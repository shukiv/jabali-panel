<?php

declare(strict_types=1);

namespace App\FileBrowser\ValueObjects;

use InvalidArgumentException;

final readonly class FilePermission
{
    private function __construct(
        private int $owner,
        private int $group,
        private int $other,
    ) {}

    public static function fromOctal(string $octal): self
    {
        $octal = ltrim($octal, '0') ?: '0';
        $octal = str_pad($octal, 3, '0', STR_PAD_LEFT);
        $octal = substr($octal, -3);

        foreach (str_split($octal) as $digit) {
            if ($digit < '0' || $digit > '7') {
                throw new InvalidArgumentException("Invalid octal permission string: {$octal}");
            }
        }

        return new self((int) $octal[0], (int) $octal[1], (int) $octal[2]);
    }

    public static function fromUnixString(string $perms): self
    {
        if (strlen($perms) < 10) {
            throw new InvalidArgumentException("Invalid Unix permission string: {$perms}");
        }

        return new self(
            self::parseTriad($perms[1], $perms[2], $perms[3]),
            self::parseTriad($perms[4], $perms[5], $perms[6]),
            self::parseTriad($perms[7], $perms[8], $perms[9]),
        );
    }

    public static function fromToggles(array $data): self
    {
        return new self(
            self::boolsToTriad($data['owner_read'] ?? false, $data['owner_write'] ?? false, $data['owner_execute'] ?? false),
            self::boolsToTriad($data['group_read'] ?? false, $data['group_write'] ?? false, $data['group_execute'] ?? false),
            self::boolsToTriad($data['other_read'] ?? false, $data['other_write'] ?? false, $data['other_execute'] ?? false),
        );
    }

    public function toOctal(): string
    {
        return "{$this->owner}{$this->group}{$this->other}";
    }

    public function toFormState(): array
    {
        return [
            'mode' => $this->toOctal(),
            'owner_read' => (bool) ($this->owner & 4),
            'owner_write' => (bool) ($this->owner & 2),
            'owner_execute' => (bool) ($this->owner & 1),
            'group_read' => (bool) ($this->group & 4),
            'group_write' => (bool) ($this->group & 2),
            'group_execute' => (bool) ($this->group & 1),
            'other_read' => (bool) ($this->other & 4),
            'other_write' => (bool) ($this->other & 2),
            'other_execute' => (bool) ($this->other & 1),
        ];
    }

    private static function parseTriad(string $r, string $w, string $x): int
    {
        $val = 0;
        if ($r === 'r') {
            $val += 4;
        }
        if ($w === 'w') {
            $val += 2;
        }
        if ($x === 'x' || $x === 's' || $x === 't') {
            $val += 1;
        }

        return $val;
    }

    private static function boolsToTriad(bool $read, bool $write, bool $execute): int
    {
        return ($read ? 4 : 0) + ($write ? 2 : 0) + ($execute ? 1 : 0);
    }
}
