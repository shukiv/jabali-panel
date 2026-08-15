package commands

import (
	"io"
	"strings"
	"testing"
)

// JAB-239: locks the DEFINER-rewriting stream filter and the shadow
// account naming. The exec orchestration (scoped user provisioning,
// sudo -u nobody load) is box-verified — these handlers exec real
// binaries, matching the existing test style for this package.

func readDump(t *testing.T, r io.Reader) string {
	t.Helper()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(out)
}

func TestDefinerRewritingReader(t *testing.T) {
	shadow := scopedShadowUser("shop_db")

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain statements pass through unchanged",
			input: "CREATE TABLE t (id INT);\nINSERT INTO t VALUES (1);\n",
			want:  "CREATE TABLE t (id INT);\nINSERT INTO t VALUES (1);\n",
		},
		{
			name: "view definer rewritten to shadow",
			input: "CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER VIEW `v` AS SELECT 1;\n",
			want: "CREATE ALGORITHM=UNDEFINED DEFINER=`" + shadow + "`@`localhost` SQL SECURITY DEFINER VIEW `v` AS SELECT 1;\n",
		},
		{
			name: "versioned trigger comment rewritten",
			input: "/*!50017 DEFINER=`admin`@`%`*/ /*!50003 TRIGGER `tr` BEFORE INSERT ON `t` FOR EACH ROW SET @x = 1 */;;\n",
			want: "/*!50017 DEFINER=`" + shadow + "`@`localhost`*/ /*!50003 TRIGGER `tr` BEFORE INSERT ON `t` FOR EACH ROW SET @x = 1 */;;\n",
		},
		{
			name:  "lowercase definer also caught",
			input: "create definer=`u`@`%` procedure p() begin end;\n",
			want:  "create DEFINER=`" + shadow + "`@`localhost` procedure p() begin end;\n",
		},
		{
			name:  "multiple definers on one line all rewritten",
			input: "X DEFINER=`a`@`h1` DEFINER=`b`@`h2` ;\n",
			want:  "X DEFINER=`" + shadow + "`@`localhost` DEFINER=`" + shadow + "`@`localhost` ;\n",
		},
		{
			name:  "no trailing newline still flushed",
			input: "SELECT 1;",
			want:  "SELECT 1;",
		},
		{
			name:  "empty stream yields empty output",
			input: "",
			want:  "",
		},
		{
			name:  "data containing the word definer without backticks untouched",
			input: "INSERT INTO t VALUES ('definer=root@localhost');\n",
			want:  "INSERT INTO t VALUES ('definer=root@localhost');\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readDump(t, newDefinerRewritingReader(strings.NewReader(tt.input), shadow))
			if got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestDefinerRewritingReaderLongLines(t *testing.T) {
	// Extended INSERT lines in real dumps can run to megabytes — the
	// filter must not truncate or choke past bufio's default token cap.
	shadow := scopedShadowUser("big")
	long := "INSERT INTO t VALUES ('" + strings.Repeat("x", 3*1024*1024) + "');\n"
	got := readDump(t, newDefinerRewritingReader(strings.NewReader(long), shadow))
	if got != long {
		t.Errorf("long line mangled: got %d bytes, want %d", len(got), len(long))
	}
}

func TestScopedShadowUser(t *testing.T) {
	if got := scopedShadowUser("shop_db"); got != "jb_s_shop_db" {
		t.Errorf("unexpected shadow name %q", got)
	}
	// 5 + 64 = 69 chars max, under MariaDB's 80-char user limit even
	// for the longest valid database name.
	long := scopedShadowUser(strings.Repeat("a", 64))
	if len(long) > 80 {
		t.Errorf("shadow name too long: %d chars", len(long))
	}
}
