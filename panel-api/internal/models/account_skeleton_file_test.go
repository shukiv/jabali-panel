package models

import "testing"

func TestValidateSkeletonPath(t *testing.T) {
  good := []string{"index.html", "css/style.css", "a/b/c/file.txt", "assets/img/logo.png", ".htaccess"}
  for _, p := range good {
    if err := ValidateSkeletonPath(p); err != nil { t.Errorf("valid path %q rejected: %v", p, err) }
  }
  bad := []string{
    "", "/etc/passwd", "../secret", "a/../../../etc", "a/./b", "a//b", "a/",
    "..", ".", "a/..", "back\\slash", string([]byte{'a', 0, 'b'}),
  }
  for _, p := range bad {
    if err := ValidateSkeletonPath(p); err == nil { t.Errorf("BAD path %q was accepted (traversal/abs/etc)", p) }
  }
}
