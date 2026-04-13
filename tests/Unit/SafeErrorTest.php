<?php

declare(strict_types=1);

namespace Tests\Unit;

use App\Support\SafeError;
use RuntimeException;
use Tests\TestCase;

class SafeErrorTest extends TestCase
{
    public function test_message_returns_generic_text_for_non_debug(): void
    {
        config(['app.debug' => false]);

        $text = SafeError::message(new RuntimeException('raw internal path /var/lib/something/sensitive'));

        $this->assertStringNotContainsString('/var/lib/something/sensitive', $text);
        $this->assertNotEmpty($text);
    }

    public function test_message_uses_fallback_when_provided_in_non_debug(): void
    {
        config(['app.debug' => false]);

        $text = SafeError::message(new RuntimeException('leak'), 'Backup failed');

        $this->assertSame('Backup failed', $text);
    }

    public function test_message_includes_details_in_debug_mode(): void
    {
        config(['app.debug' => true]);

        $text = SafeError::message(new RuntimeException('boom'));

        $this->assertStringContainsString('RuntimeException', $text);
        $this->assertStringContainsString('boom', $text);
    }

    public function test_from_agent_returns_cleaned_string(): void
    {
        $text = SafeError::fromAgent('mount failed: No space left on device');

        $this->assertSame('mount failed: No space left on device', $text);
    }

    public function test_from_agent_collapses_newlines_and_control_chars(): void
    {
        $input = "line one\nline two\r\nline three\ttabbed\x00null";

        $text = SafeError::fromAgent($input);

        $this->assertStringNotContainsString("\n", $text);
        $this->assertStringNotContainsString("\x00", $text);
        $this->assertStringNotContainsString("\t", $text);
        $this->assertSame('line one line two line three tabbed null', $text);
    }

    public function test_from_agent_returns_fallback_for_empty_input(): void
    {
        $text = SafeError::fromAgent('', 'Unknown agent error');

        $this->assertSame('Unknown agent error', $text);
    }

    public function test_from_agent_returns_fallback_for_whitespace_only(): void
    {
        $text = SafeError::fromAgent("   \n\t  ", 'Unknown agent error');

        $this->assertSame('Unknown agent error', $text);
    }

    public function test_from_agent_truncates_long_messages(): void
    {
        $long = str_repeat('a', 1000);

        $text = SafeError::fromAgent($long);

        $this->assertLessThanOrEqual(500, mb_strlen($text));
        $this->assertStringEndsWith('...', $text);
    }

    public function test_from_agent_uses_default_fallback_when_none_given(): void
    {
        $text = SafeError::fromAgent('');

        $this->assertNotEmpty($text);
    }
}
