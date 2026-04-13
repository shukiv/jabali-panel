<?php

declare(strict_types=1);

namespace Tests\Unit;

use App\FileBrowser\ValueObjects\FilePermission;
use InvalidArgumentException;
use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\TestCase;

class FilePermissionTest extends TestCase
{
    // --- fromOctal() ---

    public function test_from_octal_parses_755(): void
    {
        $perm = FilePermission::fromOctal('755');

        $this->assertSame('755', $perm->toOctal());
    }

    public function test_from_octal_handles_leading_zero(): void
    {
        $perm = FilePermission::fromOctal('0644');

        $this->assertSame('644', $perm->toOctal());
    }

    public function test_from_octal_handles_double_leading_zero(): void
    {
        $perm = FilePermission::fromOctal('00777');

        $this->assertSame('777', $perm->toOctal());
    }

    public function test_from_octal_parses_000(): void
    {
        $perm = FilePermission::fromOctal('000');

        $this->assertSame('000', $perm->toOctal());
    }

    public function test_from_octal_rejects_invalid_digit(): void
    {
        $this->expectException(InvalidArgumentException::class);

        FilePermission::fromOctal('898');
    }

    // --- toOctal() ---

    public function test_to_octal_returns_three_digit_string(): void
    {
        $perm = FilePermission::fromOctal('644');

        $this->assertSame('644', $perm->toOctal());
        $this->assertSame(3, strlen($perm->toOctal()));
    }

    // --- fromToggles() ---

    public function test_from_toggles_all_true_produces_777(): void
    {
        $perm = FilePermission::fromToggles([
            'owner_read' => true, 'owner_write' => true, 'owner_execute' => true,
            'group_read' => true, 'group_write' => true, 'group_execute' => true,
            'other_read' => true, 'other_write' => true, 'other_execute' => true,
        ]);

        $this->assertSame('777', $perm->toOctal());
    }

    public function test_from_toggles_all_false_produces_000(): void
    {
        $perm = FilePermission::fromToggles([
            'owner_read' => false, 'owner_write' => false, 'owner_execute' => false,
            'group_read' => false, 'group_write' => false, 'group_execute' => false,
            'other_read' => false, 'other_write' => false, 'other_execute' => false,
        ]);

        $this->assertSame('000', $perm->toOctal());
    }

    public function test_from_toggles_typical_644(): void
    {
        $perm = FilePermission::fromToggles([
            'owner_read' => true, 'owner_write' => true, 'owner_execute' => false,
            'group_read' => true, 'group_write' => false, 'group_execute' => false,
            'other_read' => true, 'other_write' => false, 'other_execute' => false,
        ]);

        $this->assertSame('644', $perm->toOctal());
    }

    public function test_from_toggles_typical_755(): void
    {
        $perm = FilePermission::fromToggles([
            'owner_read' => true, 'owner_write' => true, 'owner_execute' => true,
            'group_read' => true, 'group_write' => false, 'group_execute' => true,
            'other_read' => true, 'other_write' => false, 'other_execute' => true,
        ]);

        $this->assertSame('755', $perm->toOctal());
    }

    public function test_from_toggles_missing_keys_default_to_false(): void
    {
        $perm = FilePermission::fromToggles([
            'owner_read' => true,
        ]);

        $this->assertSame('400', $perm->toOctal());
    }

    // --- fromUnixString() ---

    public function test_from_unix_string_parses_drwxr_xr_x(): void
    {
        $perm = FilePermission::fromUnixString('drwxr-xr-x');

        $this->assertSame('755', $perm->toOctal());
    }

    public function test_from_unix_string_parses_file_rw_r_r(): void
    {
        $perm = FilePermission::fromUnixString('-rw-r--r--');

        $this->assertSame('644', $perm->toOctal());
    }

    public function test_from_unix_string_rejects_short_string(): void
    {
        $this->expectException(InvalidArgumentException::class);

        FilePermission::fromUnixString('rwx');
    }

    // --- Round-trip ---

    #[DataProvider('octalRoundTripValues')]
    public function test_round_trip_from_octal_to_octal(string $octal): void
    {
        $this->assertSame($octal, FilePermission::fromOctal($octal)->toOctal());
    }

    /** @return array<string, array{string}> */
    public static function octalRoundTripValues(): array
    {
        return [
            '000' => ['000'],
            '644' => ['644'],
            '755' => ['755'],
            '777' => ['777'],
            '400' => ['400'],
            '600' => ['600'],
            '700' => ['700'],
            '750' => ['750'],
        ];
    }

    // --- toFormState() ---

    public function test_to_form_state_returns_correct_toggles_for_755(): void
    {
        $state = FilePermission::fromOctal('755')->toFormState();

        $this->assertSame('755', $state['mode']);
        $this->assertTrue($state['owner_read']);
        $this->assertTrue($state['owner_write']);
        $this->assertTrue($state['owner_execute']);
        $this->assertTrue($state['group_read']);
        $this->assertFalse($state['group_write']);
        $this->assertTrue($state['group_execute']);
        $this->assertTrue($state['other_read']);
        $this->assertFalse($state['other_write']);
        $this->assertTrue($state['other_execute']);
    }

    public function test_form_state_round_trip(): void
    {
        $original = FilePermission::fromOctal('640');
        $state = $original->toFormState();
        $restored = FilePermission::fromToggles($state);

        $this->assertSame($original->toOctal(), $restored->toOctal());
    }
}
