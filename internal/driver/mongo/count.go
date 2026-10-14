package mongo

import (
	"context"
	"errors"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/gxd3v/quarry/internal/driver"
)

var _ driver.Counter = (*conn)(nil)

// CountRows counts the documents of a collection or view matching filter.
func (c *conn) CountRows(ctx context.Context, target *driver.Node, filter string) (int64, error) {
	db, coll, err := collectionNode(target)
	if err != nil {
		return 0, err
	}
	if err := c.check(); err != nil {
		return 0, err
	}
	f := bson.D{}
	if strings.Trim(strings.TrimSpace(filter), ";") != "" {
		text, err := dataFilter(filter)
		if err != nil {
			return 0, err
		}
		v, err := parseValue(text)
		if err != nil {
			return 0, err
		}
		d, ok := v.(bson.D)
		if !ok {
			return 0, errors.New("invalid filter")
		}
		f = d
	}
	ctx, cancel := bound(ctx)
	defer cancel()
	n, err := c.client.Database(db).Collection(coll).CountDocuments(ctx, f)
	return n, queryError(err)
}
