package oracle

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/sijms/go-ora/v2/network"

	"github.com/gxd3v/quarry/internal/driver"
)

var codeRe = regexp.MustCompile(`^([A-Z]{2,5}-\d{5}): ?`)

func asOracleError(err error) *network.OracleError {
	oe, _ := errors.AsType[*network.OracleError](err)
	return oe
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// queryError converts server errors to *driver.QueryError, with Position as
// a byte offset into text; other errors are wrapped.
func queryError(err error, text string) error {
	if qe, ok := errors.AsType[*driver.QueryError](err); ok {
		return qe
	}
	if fe, ok := errors.AsType[*friendlyError](err); ok {
		return fe
	}
	oe := asOracleError(err)
	if oe == nil {
		if errors.Is(err, errClosed) || errors.Is(err, errLost) {
			return err
		}
		return fmt.Errorf("oracle: %w", err)
	}
	return oraError(oe, text)
}

// oraError splits "ORA-00942: table or view ... does not exist\nORA-06512:
// ..." into code, message and detail (the rest of the error stack).
//
// The server reports where parse errors occur as a byte offset into the
// statement text (UTF-8 for AL32UTF8 databases). It reports 0 when it has no
// position, so 0 is taken as unknown; positions of errors raised in PL/SQL
// or recursive SQL point elsewhere and are dropped too.
func oraError(oe *network.OracleError, text string) *driver.QueryError {
	msg := strings.TrimSpace(oe.ErrMsg)
	if msg == "" {
		msg = oe.Error()
	}
	first, rest, _ := strings.Cut(msg, "\n")
	qe := &driver.QueryError{Msg: strings.TrimSpace(first), Detail: strings.TrimSpace(rest), Position: -1}
	if m := codeRe.FindStringSubmatch(qe.Msg); m != nil {
		qe.Code = m[1]
		qe.Msg = qe.Msg[len(m[0]):]
	} else {
		qe.Code = fmt.Sprintf("ORA-%05d", oe.ErrCode)
	}
	pos := oe.ErrPos()
	switch {
	case pos <= 0 || pos >= len(text):
	case oe.ErrCode == 1013 || oe.ErrCode == 604:
	case strings.Contains(qe.Detail, "ORA-06512"):
	default:
		qe.Position = pos
	}
	return qe
}
