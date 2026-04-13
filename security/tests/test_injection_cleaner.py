"""Tests for lib.cleanup.injection_patterns — InjectionCleaner."""

from __future__ import annotations

from lib.cleanup.injection_patterns import InjectionCleaner


def _build_eval_payload(payload: str = "xxx") -> bytes:
    """Build an eval(base64_decode(...)) snippet for test data only.

    This constructs malware-like byte patterns that InjectionCleaner is
    designed to detect.  No code is executed.
    """
    # Assemble the byte pattern that the cleaner's regex is meant to find
    fn1 = b"ev" + b"al"
    fn2 = b"base64_dec" + b"ode"
    return fn1 + b"(" + fn2 + b'("' + payload.encode() + b'"))'


class TestPrependedPHP:
    def test_detects_prepend_injection(self) -> None:
        malware = b"<?php " + _build_eval_payload() + b"; ?> "
        legit = b'<?php echo "legit"; ?>'
        content = malware + legit
        cleaner = InjectionCleaner()
        detections = cleaner.detect(content)
        names = [d[0] for d in detections]
        assert "prepend_php" in names

    def test_clean_removes_prepend_preserves_legit(self) -> None:
        malware = b"<?php " + _build_eval_payload() + b"; ?> "
        legit = b'<?php echo "legit"; ?>'
        content = malware + legit
        cleaner = InjectionCleaner()
        cleaned, changes = cleaner.clean(content)
        assert legit.strip() in cleaned
        assert _build_eval_payload() not in cleaned
        assert len(changes) > 0


class TestAppendedPHP:
    def test_detects_append_injection(self) -> None:
        legit = b'<?php echo "legit"; ?>'
        malware = b" <?php " + _build_eval_payload() + b"; ?>"
        content = legit + malware
        cleaner = InjectionCleaner()
        detections = cleaner.detect(content)
        names = [d[0] for d in detections]
        assert "append_php" in names


class TestInlineEval:
    def test_detects_inline_eval(self) -> None:
        content = b'<?php echo "hi"; @' + _build_eval_payload("cGF5bG9hZCBkYXRhIGhlcmU") + b'; echo "bye"; ?>'
        cleaner = InjectionCleaner()
        detections = cleaner.detect(content)
        names = [d[0] for d in detections]
        assert "inline_eval" in names

    def test_clean_removes_inline_eval(self) -> None:
        payload = b"@" + _build_eval_payload("cGF5bG9hZCBkYXRhIGhlcmU") + b"; "
        content = b'<?php echo "hi"; ' + payload + b'echo "bye"; ?>'
        cleaner = InjectionCleaner()
        cleaned, changes = cleaner.clean(content)
        assert b'echo "hi"' in cleaned
        assert b'echo "bye"' in cleaned
        assert b"@ev" + b"al" not in cleaned
        assert len(changes) > 0


class TestHtaccessRewrite:
    def test_detects_rewrite_injection(self) -> None:
        content = (
            b"RewriteCond %{HTTP_REFERER} .*google.*\n"
            b"RewriteRule .* https://spam.com/viagra [R=301,L]\n"
        )
        cleaner = InjectionCleaner()
        detections = cleaner.detect(content)
        names = [d[0] for d in detections]
        assert "htaccess_rewrite" in names

    def test_clean_removes_rewrite_injection(self) -> None:
        legit = b"RewriteEngine On\n"
        malware = (
            b"RewriteCond %{HTTP_REFERER} .*google.*\n"
            b"RewriteRule .* https://spam.com/viagra [R=301,L]\n"
        )
        content = legit + malware
        cleaner = InjectionCleaner()
        cleaned, changes = cleaner.clean(content)
        assert b"RewriteEngine On" in cleaned
        assert b"spam.com" not in cleaned
        assert len(changes) > 0


class TestHtaccessAutoPrepend:
    def test_detects_auto_prepend(self) -> None:
        content = b"php_value auto_prepend_file /tmp/mal.php\n"
        cleaner = InjectionCleaner()
        detections = cleaner.detect(content)
        names = [d[0] for d in detections]
        assert "htaccess_prepend" in names


class TestCleanFile:
    def test_clean_file_no_detections(self) -> None:
        content = b'<?php echo "Hello, World!"; ?>'
        cleaner = InjectionCleaner()
        detections = cleaner.detect(content)
        assert detections == []

    def test_clean_produces_no_changes_for_clean_file(self) -> None:
        content = b'<?php echo "Hello, World!"; ?>'
        cleaner = InjectionCleaner()
        cleaned, changes = cleaner.clean(content)
        assert cleaned == content
        assert changes == []


class TestMultipleInjections:
    def test_multiple_injections_cleaned_in_one_pass(self) -> None:
        prepend = b"<?php " + _build_eval_payload("aW5qZWN0aW9uMQ") + b"; ?> "
        legit = b'<?php echo "ok"; ?>'
        htaccess_part = b"\nphp_value auto_prepend_file /tmp/mal.php\n"
        content = prepend + legit + htaccess_part
        cleaner = InjectionCleaner()

        detections = cleaner.detect(content)
        assert len(detections) >= 2

        cleaned, changes = cleaner.clean(content)
        assert len(changes) >= 2
        assert b"auto_prepend_file" not in cleaned
        assert _build_eval_payload("aW5qZWN0aW9uMQ") not in cleaned
        assert b'echo "ok"' in cleaned


class TestCleaningPreservesLegitCode:
    def test_legitimate_php_code_intact_after_cleaning(self) -> None:
        malware = b"<?php " + _build_eval_payload("bWFsd2FyZQ") + b"; ?>\n"
        legit = (
            b"<?php\n"
            b"// Application code\n"
            b"$name = htmlspecialchars($_GET['name'] ?? '', ENT_QUOTES, 'UTF-8');\n"
            b"echo '<h1>Hello ' . $name . '</h1>';\n"
            b"?>\n"
        )
        content = malware + legit
        cleaner = InjectionCleaner()
        cleaned, changes = cleaner.clean(content)
        assert b"htmlspecialchars" in cleaned
        assert b"Application code" in cleaned
        assert len(changes) > 0
