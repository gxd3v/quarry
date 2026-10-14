package mongo

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Explainer = (*session)(nil)

// Explain returns the winning plan of a collection query (find, findOne,
// aggregate, countDocuments, distinct, update*, replaceOne, delete*,
// findOneAnd*). analyze uses the executionStats verbosity, which runs
// the query but never applies writes; nodes then carry nReturned (ActRows)
// and executionTimeMillisEstimate (Actual). MongoDB has no cost or row
// estimates, so Cost and Rows are 0. Aggregations whose stages are not
// pushed into the query engine show one node per stage, the last on top.
func (s *session) Explain(ctx context.Context, stmt driver.Statement, analyze bool) (*driver.PlanNode, error) {
	s.closeCur()
	if err := s.check(); err != nil {
		return nil, err
	}
	st, err := parseStatement(stmt.Text)
	if err != nil {
		return nil, queryError(err)
	}
	if st.kind != kDB || !st.hasColl {
		return nil, &driver.QueryError{Msg: "only collection queries (find, aggregate, ...) can be explained", Position: 0}
	}
	calls := st.calls
	if calls[0].name == "explain" && len(calls) > 1 {
		calls = calls[1:]
	}
	db := s.db
	if st.hasSibling {
		db = st.sibling
	}
	x := &executor{s: s, db: s.c.client.Database(db), coll: st.coll}
	q, ok, err := x.build(calls[0], calls[1:])
	if err != nil {
		return nil, queryError(err)
	}
	if !ok {
		return nil, &driver.QueryError{Msg: calls[0].name + " cannot be explained", Position: calls[0].pos}
	}
	verbosity := "queryPlanner"
	if analyze {
		verbosity = "executionStats"
	}
	var reply bson.Raw
	err = s.interruptible(ctx, func(ctx context.Context) error {
		var err error
		reply, err = x.command(ctx, false, bson.D{{Key: "explain", Value: q.cmd}, {Key: "verbosity", Value: verbosity}})
		return err
	})
	if err != nil {
		s.afterError(ctx, err)
		return nil, queryError(err)
	}
	return planTree(reply, analyze), nil
}

// planTree converts an explain reply.
func planTree(reply bson.Raw, analyze bool) *driver.PlanNode {
	if stages, ok := reply.Lookup("stages").ArrayOK(); ok {
		return stagesTree(stages, analyze)
	}
	qp, _ := reply.Lookup("queryPlanner").DocumentOK()
	wp, _ := qp.Lookup("winningPlan").DocumentOK()
	if inner, ok := wp.Lookup("queryPlan").DocumentOK(); ok { // slot-based engine
		wp = inner
	}
	root := planNode(wp, analyze)
	if !analyze {
		return root
	}
	es, ok := reply.Lookup("executionStats").DocumentOK()
	if !ok {
		return root
	}
	if stages, ok := es.Lookup("executionStages").DocumentOK(); ok && stageName(stages) == root.Op {
		root = planNode(stages, true)
	}
	if v, err := es.LookupErr("executionTimeMillis"); err == nil {
		root.Actual = v.AsFloat64()
	}
	if v, err := es.LookupErr("nReturned"); err == nil {
		root.ActRows = v.AsFloat64()
	}
	for _, k := range []string{"totalKeysExamined", "totalDocsExamined"} {
		if v, err := es.LookupErr(k); err == nil {
			root.Props[k] = v.String()
		}
	}
	return root
}

func stageName(doc bson.Raw) string {
	s, _ := doc.Lookup("stage").StringValueOK()
	return s
}

// planNoise are explain fields not worth showing.
var planNoise = map[string]bool{
	"works": true, "advanced": true, "needTime": true, "needYield": true, "saveState": true,
	"restoreState": true, "isEOF": true, "planNodeId": true, "isCached": true, "optimizationTimeMillis": true,
}

// planNode converts one plan stage; fields ending in Stage or Stages
// (inputStage, inputStages, innerStage, ...) are its children.
func planNode(doc bson.Raw, analyze bool) *driver.PlanNode {
	n := &driver.PlanNode{Op: stageName(doc), Actual: -1, ActRows: -1, Props: map[string]string{}}
	els, _ := doc.Elements()
	for _, e := range els {
		k, v := e.Key(), e.Value()
		switch {
		case k == "stage" || planNoise[k]:
		case k == "indexName":
			n.Target, _ = v.StringValueOK()
		case strings.HasSuffix(k, "Stage") && v.Type == bson.TypeEmbeddedDocument:
			n.Children = append(n.Children, planNode(v.Document(), analyze))
		case strings.HasSuffix(k, "Stages") && v.Type == bson.TypeArray:
			vals, _ := v.Array().Values()
			for _, c := range vals {
				if d, ok := c.DocumentOK(); ok {
					n.Children = append(n.Children, planNode(d, analyze))
				}
			}
		case k == "nReturned" && analyze:
			n.ActRows = v.AsFloat64()
		case k == "executionTimeMillisEstimate" && analyze:
			n.Actual = v.AsFloat64()
		default:
			n.Props[k] = propText(v)
		}
	}
	return n
}

func propText(v bson.RawValue) string {
	if s, ok := v.StringValueOK(); ok {
		return s
	}
	return formatJS(v)
}

// stagesTree chains the stages of an aggregation explain: each stage's
// child is the stage before it, and $cursor holds the query plan.
func stagesTree(stages bson.RawArray, analyze bool) *driver.PlanNode {
	vals, _ := stages.Values()
	var prev *driver.PlanNode
	for _, sv := range vals {
		doc, ok := sv.DocumentOK()
		if !ok {
			continue
		}
		els, _ := doc.Elements()
		if len(els) == 0 {
			continue
		}
		name, spec := els[0].Key(), els[0].Value()
		n := &driver.PlanNode{Op: name, Actual: -1, ActRows: -1, Props: map[string]string{}}
		if inner, ok := spec.DocumentOK(); ok && name == "$cursor" {
			n.Children = append(n.Children, planTree(inner, analyze))
		} else {
			n.Props["spec"] = propText(spec)
		}
		for _, e := range els[1:] {
			switch e.Key() {
			case "nReturned":
				if analyze {
					n.ActRows = e.Value().AsFloat64()
				}
			case "executionTimeMillisEstimate":
				if analyze {
					n.Actual = e.Value().AsFloat64()
				}
			default:
				n.Props[e.Key()] = propText(e.Value())
			}
		}
		if prev != nil {
			n.Children = append(n.Children, prev)
		}
		prev = n
	}
	if prev == nil {
		return &driver.PlanNode{Op: "aggregate", Actual: -1, ActRows: -1, Props: map[string]string{}}
	}
	return prev
}
