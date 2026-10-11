package commands

import "git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"

// checkHostReserve is the host disk reserve check for every handler in
// package commands. Production code calls hostreserve.CheckReserve through
// it unchanged; the test binary replaces it in TestMain (see
// exec_seam_test.go), because the free space on the machine running the
// tests is not a test input: on a nearly full disk every test that gets
// past a reserve check failed.
//
// Do NOT call hostreserve.CheckReserve directly in non-test files;
// TestReserveSeam_NoRawCheck enforces that.
var checkHostReserve = hostreserve.CheckReserve
