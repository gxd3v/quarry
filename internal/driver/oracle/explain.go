package oracle

import (
	"context"
	"crypto/rand"
	sqldriver "database/sql/driver"
	"errors"
	"strconv"
	"strings"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Explainer = (*session)(nil)

const planQuery = `SELECT id, NVL(parent_id, -1), operation, options, object_owner, object_name,
  object_alias, cost, cardinality, bytes, access_predicates, filter_predicates, partition_start,
  partition_stop
FROM plan_table WHERE statement_id = :1 ORDER BY id`

// Explain shows the optimizer's plan with EXPLAIN PLAN. Analyzing would
// need the statement's runtime statistics from V$SQL_PLAN_STATISTICS_ALL,
// which ordinary users cannot read, so it is refused. The PLAN_TABLE rows are
// removed afterwards; EXPLAIN PLAN opens a transaction, which is rolled back
// unless the user already had one open.
func (s *session) Explain(ctx context.Context, stmt driver.Statement, analyze bool) (*driver.PlanNode, error) {
	s.closeCur()
	if analyze {
		return nil, errors.New("explain analyze is not available for Oracle; a plain explain is")
	}
	toks := codeTokens(stmt.Text)
	if len(toks) == 0 {
		return nil, errors.New("nothing to explain")
	}
	if unit, _ := unitKind(toks); unit || toks[0].kind == tokWord && isSQLPlus(toks, 0) {
		return nil, errors.New("only SQL statements can be explained")
	}
	if !s.link.alive() {
		return nil, errLost
	}

	id := "quarry" + strings.ToLower(rand.Text()[:12])
	prefix := "EXPLAIN PLAN SET STATEMENT_ID = '" + id + "' FOR "
	text := execText(stmt.Text, toks, false)
	hadTx := s.inTx.Load()

	var rows []planRow
	err := s.call(ctx, prefix+text, func() error {
		if _, err := s.link.oc.ExecContext(ctx, prefix+text, nil); err != nil {
			return err
		}
		return s.link.query(ctx, planQuery, []any{id}, func(r []sqldriver.Value) error {
			rows = append(rows, planRowOf(r))
			return nil
		})
	})
	if !hadTx {
		_, _ = s.link.exec(context.Background(), "ROLLBACK")
	} else {
		_, _ = s.link.exec(context.Background(), "DELETE FROM plan_table WHERE statement_id = :1", id)
	}
	if err != nil {
		err = s.failure(err, prefix+text)
		if qe, ok := errors.AsType[*driver.QueryError](err); ok && qe.Position >= 0 {
			if qe.Position -= len(prefix); qe.Position < 0 {
				qe.Position = -1
			}
		}
		return nil, err
	}
	return buildPlan(rows)
}

type planRow struct {
	id, parent                        int64
	op, options, owner, object, alias string
	cost, card, bytes                 float64
	hasCost, hasCard                  bool
	access, filter, pstart, pstop     string
}

func planRowOf(r []sqldriver.Value) planRow {
	return planRow{
		id: integer(r[0]), parent: integer(r[1]),
		op: str(r[2]), options: str(r[3]), owner: str(r[4]), object: str(r[5]), alias: str(r[6]),
		cost: float(r[7]), hasCost: r[7] != nil,
		card: float(r[8]), hasCard: r[8] != nil,
		bytes:  float(r[9]),
		access: str(r[10]), filter: str(r[11]), pstart: str(r[12]), pstop: str(r[13]),
	}
}

// buildPlan links PLAN_TABLE rows into a tree rooted at id 0.
func buildPlan(rows []planRow) (*driver.PlanNode, error) {
	nodes := make(map[int64]*driver.PlanNode, len(rows))
	var root *driver.PlanNode
	for _, r := range rows {
		n := &driver.PlanNode{Op: strings.TrimSpace(r.op + " " + r.options), Actual: -1, ActRows: -1, Props: map[string]string{}}
		if r.object != "" {
			n.Target = r.object
			if r.owner != "" {
				n.Target = r.owner + "." + r.object
			}
			if a, _, _ := strings.Cut(r.alias, "@"); a != "" && a != r.object && a != `"`+r.object+`"` {
				n.Target += " " + a
			}
		}
		if r.hasCost {
			n.Cost = r.cost
		}
		if r.hasCard {
			n.Rows = r.card
		}
		if r.bytes > 0 {
			n.Props["Bytes"] = strconv.FormatFloat(r.bytes, 'f', -1, 64)
		}
		if r.access != "" {
			n.Props["Access"] = r.access
		}
		if r.filter != "" {
			n.Props["Filter"] = r.filter
		}
		if r.pstart != "" {
			n.Props["Partitions"] = r.pstart + " .. " + r.pstop
		}
		nodes[r.id] = n
		if r.parent < 0 {
			root = n
		} else if p := nodes[r.parent]; p != nil {
			p.Children = append(p.Children, n)
		} else {
			return nil, errors.New("oracle: plan row " + strconv.FormatInt(r.id, 10) + " comes before its parent")
		}
	}
	if root == nil {
		return nil, errors.New("oracle: the plan is empty")
	}
	return root, nil
}
