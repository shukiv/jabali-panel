<?php

declare(strict_types=1);

namespace App\Support;

class Formatter
{
    public static function bytes(int|float $bytes, int $precision = 1): string
    {
        $units = ['B', 'KB', 'MB', 'GB', 'TB'];
        $size = max(0, (float) $bytes);
        $unit = 0;

        while ($size >= 1024 && $unit < count($units) - 1) {
            $size /= 1024;
            $unit++;
        }

        return round($size, $precision).' '.$units[$unit];
    }
}
