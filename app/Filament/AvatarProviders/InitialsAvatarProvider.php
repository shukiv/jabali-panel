<?php

declare(strict_types=1);

namespace App\Filament\AvatarProviders;

use Filament\AvatarProviders\Contracts\AvatarProvider;
use Illuminate\Contracts\Auth\Authenticatable;
use Illuminate\Database\Eloquent\Model;

class InitialsAvatarProvider implements AvatarProvider
{
    public function get(Model|Authenticatable $record): string
    {
        $name = $record->name ?? $record->email ?? 'U';
        $initials = $this->getInitials($name);

        // Generate a consistent color based on the name
        $hash = crc32($name);
        $hue = $hash % 360;

        // Generate SVG avatar
        $svg = $this->generateSvg($initials, $hue);

        return 'data:image/svg+xml;base64,'.base64_encode($svg);
    }

    private function getInitials(string $name): string
    {
        $words = preg_split('/[\s@._-]+/', trim($name));
        $initials = '';

        foreach ($words as $word) {
            if (! empty($word)) {
                $initials .= mb_strtoupper(mb_substr($word, 0, 1));
                if (mb_strlen($initials) >= 2) {
                    break;
                }
            }
        }

        return $initials ?: 'U';
    }

    private function generateSvg(string $initials, int $hue): string
    {
        $bgColor = "hsl({$hue}, 50%, 50%)";

        return <<<SVG
<svg xmlns="http://www.w3.org/2000/svg" width="128" height="128" viewBox="0 0 128 128">
    <rect width="128" height="128" fill="{$bgColor}" rx="64"/>
    <text x="64" y="64" dominant-baseline="central" text-anchor="middle" fill="white" font-family="system-ui, -apple-system, sans-serif" font-size="48" font-weight="600">{$initials}</text>
</svg>
SVG;
    }
}
