package repository

import (
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
)

// The migration 000308 triggers refuse postmaster@ on a tenant domain with
// SIGNAL; the repositories report that as mailaddr.ErrPostmasterReserved and
// leave every other error alone.
func TestMapPostmasterReserved(t *testing.T) {
	refusal := &mysql.MySQLError{Number: 1644, Message: "postmaster@ belongs to the server administrator"}
	if err := mapPostmasterReserved(refusal); !errors.Is(err, mailaddr.ErrPostmasterReserved) {
		t.Fatalf("trigger refusal: got %v, want ErrPostmasterReserved", err)
	}
	for _, other := range []error{
		nil,
		&mysql.MySQLError{Number: 1644, Message: "some other signal"},
		&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"},
		errors.New("postmaster@ belongs to the server administrator"),
	} {
		if got := mapPostmasterReserved(other); got != other {
			t.Errorf("mapPostmasterReserved(%v) = %v, want it unchanged", other, got)
		}
	}
}
