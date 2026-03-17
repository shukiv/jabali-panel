<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Support\Facades\Cache;

class UserSetting extends Model
{
    protected $fillable = [
        'user_id',
        'key',
        'value',
    ];

    protected function casts(): array
    {

        return [
            'value' => 'json',
        ];

    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public static function getForUser(int $userId, string $key, mixed $default = null): mixed
    {
        return Cache::remember("user_setting.{$userId}.{$key}", 3600, function () use ($userId, $key, $default) {
            return static::query()
                ->where('user_id', $userId)
                ->where('key', $key)
                ->value('value') ?? $default;
        });
    }

    public static function setForUser(int $userId, string $key, mixed $value): void
    {
        static::updateOrCreate(
            ['user_id' => $userId, 'key' => $key],
            ['value' => $value]
        );

        Cache::forget("user_setting.{$userId}.{$key}");
    }

    public static function forgetForUser(int $userId, string $key): void
    {
        static::query()
            ->where('user_id', $userId)
            ->where('key', $key)
            ->delete();

        Cache::forget("user_setting.{$userId}.{$key}");
    }

    public static function getAllForUser(int $userId): array
    {
        return static::query()
            ->where('user_id', $userId)
            ->pluck('value', 'key')
            ->toArray();
    }
}
