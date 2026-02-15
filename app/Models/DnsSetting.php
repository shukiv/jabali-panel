<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Facades\Cache;

class DnsSetting extends Model
{
    protected $fillable = ['key', 'value'];

    public static function get(string $key, mixed $default = null): mixed
    {
        // Cache failures should never take the panel down. If the cache store is misconfigured
        // or temporarily unavailable (e.g., SQLite cache table can't be written), fall back
        // to a direct DB read.
        try {
            return Cache::remember("dns_setting_{$key}", 3600, function () use ($key, $default) {
                $setting = static::where('key', $key)->first();

                return $setting?->value ?? $default;
            });
        } catch (\Throwable) {
            $setting = static::where('key', $key)->first();

            return $setting?->value ?? $default;
        }
    }

    public static function set(string $key, mixed $value): void
    {
        static::updateOrCreate(['key' => $key], ['value' => $value]);
        try {
            Cache::forget("dns_setting_{$key}");
        } catch (\Throwable) {
            // Best-effort.
        }
    }

    public static function getAll(): array
    {
        try {
            return Cache::remember('dns_settings_all', 3600, function () {
                return static::pluck('value', 'key')->toArray();
            });
        } catch (\Throwable) {
            return static::pluck('value', 'key')->toArray();
        }
    }

    public static function clearCache(): void
    {
        try {
            $settings = static::pluck('key');

            foreach ($settings as $key) {
                try {
                    Cache::forget("dns_setting_{$key}");
                } catch (\Throwable) {
                    // Best-effort.
                }
            }

            Cache::forget('dns_settings_all');
        } catch (\Throwable) {
            // Best-effort.
        }
    }
}
