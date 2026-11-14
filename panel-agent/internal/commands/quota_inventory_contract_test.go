package commands

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
)

// inventoryReply answers every call with the given reply.
type inventoryReply json.RawMessage

func (r inventoryReply) Call(context.Context, string, any) (json.RawMessage, error) {
	return json.RawMessage(r), nil
}

// The panel reads this handler's reply with limits.ReadHostDisks to set a
// user's own disk limit next to its isolated FTP accounts' quotas. Pin that it
// reads the reply as the handler writes it — including a user over its hard
// limit, the case `quota` can't report — and a partial listing.
func TestQuotaInventory_ReadByTheLimitsPanelSide(t *testing.T) {
	const csv = "User,BlockStatus,FileStatus,BlockUsed,BlockSoftLimit,BlockHardLimit,BlockGrace,FileUsed,FileSoftLimit,FileHardLimit,FileGrace\n" +
		"shop,+-,--,1126400,1024000,1024000,6days,120,0,0,\n" +
		"blog,--,--,2048,0,0,,10,0,0,\n" +
		"bad,--,--,x,0,0,,1,0,0,\n"
	entries, partial := parseRepquotaCSV(csv)
	raw, err := json.Marshal(quotaInventoryResponse{Mount: "/", Entries: entries, Partial: partial})
	if err != nil {
		t.Fatal(err)
	}

	disks, err := limits.ReadHostDisks(context.Background(), inventoryReply(raw), "/")
	if !errors.Is(err, limits.ErrHostDiskPartial) {
		t.Fatalf("err = %v, want ErrHostDiskPartial for the garbled row", err)
	}
	if got := disks["shop"]; got != (limits.HostDisk{UsedKB: 1126400, LimitKB: 1024000}) {
		t.Errorf("shop (over its hard limit) = %+v, want used 1126400 KB of 1024000", got)
	}
	if got := disks["blog"]; got != (limits.HostDisk{UsedKB: 2048}) {
		t.Errorf("blog = %+v, want used 2048 KB, no limit", got)
	}
	if _, ok := limits.HostDiskOf(disks, err, "bad"); ok {
		t.Error("a user missing from a partial listing read as known")
	}
}
