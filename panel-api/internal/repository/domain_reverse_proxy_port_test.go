package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

const proxyDomainID = "01DOMAIN0000000000000000AB"

func expectProxyDomainLocked(mock sqlmock.Sqlmock, port int) {
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `id`,`reverse_proxy_port` FROM `domains` WHERE id = .+ FOR UPDATE").
		WithArgs(proxyDomainID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "reverse_proxy_port"}).AddRow(proxyDomainID, port))
}

// GH #2544: the reservation and the domain row move to the new port in one
// transaction.
func TestDomain_SetReverseProxyPortMovesBoth(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	expectProxyDomainLocked(mock, 3000)
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `port_allocations` WHERE .+ FOR UPDATE").
		WithArgs(ReverseProxyBindIface, ReverseProxyProto, 5000, "reverse_proxy", proxyDomainID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("UPDATE `port_allocations` SET `port`=.+ WHERE owner_kind = .+").
		WithArgs(5000, "reverse_proxy", proxyDomainID, ReverseProxyBindIface, ReverseProxyProto).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE `domains` SET `reverse_proxy_port`=.+ WHERE id = ").
		WithArgs(5000, sqlmock.AnyArg(), proxyDomainID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, NewDomainRepository(db).SetReverseProxyPort(context.Background(), proxyDomainID, 5000))
	require.NoError(t, mock.ExpectationsWereMet())
}

// Another owner's reservation refuses, and nothing is written.
func TestDomain_SetReverseProxyPortRefusesATakenPort(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	expectProxyDomainLocked(mock, 3000)
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM `port_allocations` WHERE .+ FOR UPDATE").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	err := NewDomainRepository(db).SetReverseProxyPort(context.Background(), proxyDomainID, 5000)
	require.True(t, errors.Is(err, ErrPortInUse), "err = %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// A docroot domain has no port to move.
func TestDomain_SetReverseProxyPortRefusesADocrootDomain(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	expectProxyDomainLocked(mock, 0)
	mock.ExpectRollback()

	err := NewDomainRepository(db).SetReverseProxyPort(context.Background(), proxyDomainID, 5000)
	require.True(t, errors.Is(err, ErrNotReverseProxy), "err = %v", err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// The current port writes nothing.
func TestDomain_SetReverseProxyPortSamePortIsANoOp(t *testing.T) {
	db, mock, raw := newMockDB(t)
	defer raw.Close()

	expectProxyDomainLocked(mock, 5000)
	mock.ExpectCommit()

	require.NoError(t, NewDomainRepository(db).SetReverseProxyPort(context.Background(), proxyDomainID, 5000))
	require.NoError(t, mock.ExpectationsWereMet())
}
