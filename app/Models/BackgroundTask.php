<?php

declare(strict_types=1);

namespace App\Models;

use App\Enums\BackgroundTaskStatus;
use App\Enums\BackgroundTaskType;
use DomainException;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Database\Eloquent\Concerns\HasUuids;
use Illuminate\Database\Eloquent\Factories\HasFactory;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\MorphTo;
use Illuminate\Support\Carbon;
use Illuminate\Support\Str;

/**
 * Unified background task record — the source of truth for any long-running
 * operation in Jabali (backup, SSL, addon install, migration, deploy).
 *
 * See docs/adr/0007-agent-v2-control-plane.md for the full design.
 */
class BackgroundTask extends Model
{
    use HasFactory;
    use HasUuids;

    protected $fillable = [
        'id',
        'type',
        'target_type',
        'target_id',
        'status',
        'progress',
        'step',
        'started_at',
        'finished_at',
        'systemd_unit',
        'pid',
        'exit_code',
        'error_message',
        'retries',
        'max_retries',
        'payload',
        'log_path',
        'callback_token',
        'dedupe_key',
    ];

    protected function casts(): array
    {
        return [
            'type' => BackgroundTaskType::class,
            'status' => BackgroundTaskStatus::class,
            'progress' => 'integer',
            'pid' => 'integer',
            'exit_code' => 'integer',
            'retries' => 'integer',
            'max_retries' => 'integer',
            'payload' => 'array',
            'started_at' => 'datetime',
            'finished_at' => 'datetime',
        ];
    }

    protected static function boot(): void
    {
        parent::boot();

        static::creating(function (self $task): void {
            if (empty($task->id)) {
                $task->id = (string) Str::uuid();
            }

            if (empty($task->callback_token)) {
                $task->callback_token = bin2hex(random_bytes(32));
            }

            if (empty($task->log_path)) {
                $task->log_path = "/var/lib/jabali/jobs/{$task->id}.log";
            }

            if (! isset($task->attributes['status'])) {
                $task->status = BackgroundTaskStatus::Pending;
            }
        });
    }

    public function target(): MorphTo
    {
        return $this->morphTo();
    }

    /* ------------------------------------------------------------------ */
    /* State transitions */
    /* ------------------------------------------------------------------ */

    public function markRunning(int $pid, string $systemdUnit): void
    {
        $this->guardNotTerminal(__FUNCTION__);

        $this->update([
            'status' => BackgroundTaskStatus::Running,
            'pid' => $pid,
            'systemd_unit' => $systemdUnit,
            'started_at' => $this->started_at ?? Carbon::now(),
        ]);
    }

    public function markProgress(int $percent, ?string $step = null): void
    {
        $this->guardNotTerminal(__FUNCTION__);

        $percent = max(0, min(100, $percent));

        $attrs = ['progress' => $percent];
        if ($step !== null) {
            $attrs['step'] = $step;
        }

        $this->update($attrs);
    }

    public function markStep(string $step): void
    {
        $this->guardNotTerminal(__FUNCTION__);

        $this->update(['step' => $step]);
    }

    public function markDone(int $exitCode = 0): void
    {
        $this->guardNotTerminal(__FUNCTION__);

        $this->update([
            'status' => BackgroundTaskStatus::Done,
            'exit_code' => $exitCode,
            'progress' => 100,
            'finished_at' => Carbon::now(),
        ]);
    }

    public function markFailed(int $exitCode, ?string $errorMessage = null): void
    {
        $this->guardNotTerminal(__FUNCTION__);

        $this->update([
            'status' => BackgroundTaskStatus::Failed,
            'exit_code' => $exitCode,
            'error_message' => $this->truncateError($errorMessage),
            'finished_at' => Carbon::now(),
        ]);
    }

    public function markCanceled(?string $reason = null): void
    {
        $this->guardNotTerminal(__FUNCTION__);

        $this->update([
            'status' => BackgroundTaskStatus::Canceled,
            'error_message' => $this->truncateError($reason),
            'finished_at' => Carbon::now(),
        ]);
    }

    private function guardNotTerminal(string $transition): void
    {
        if ($this->status instanceof BackgroundTaskStatus && $this->status->isTerminal()) {
            throw new DomainException(
                "Cannot {$transition} — task {$this->id} is already in terminal status {$this->status->value}"
            );
        }
    }

    private function truncateError(?string $error): ?string
    {
        if ($error === null) {
            return null;
        }

        return strlen($error) > 4096 ? substr($error, 0, 4093).'...' : $error;
    }

    /* ------------------------------------------------------------------ */
    /* Queries */
    /* ------------------------------------------------------------------ */

    public function scopeActive(Builder $query): Builder
    {
        return $query->whereIn('status', [
            BackgroundTaskStatus::Pending->value,
            BackgroundTaskStatus::Running->value,
        ]);
    }

    public function scopeOfType(Builder $query, BackgroundTaskType $type): Builder
    {
        return $query->where('type', $type->value);
    }

    public function scopeForTarget(Builder $query, string $type, string $id): Builder
    {
        return $query->where('target_type', $type)->where('target_id', $id);
    }

    /* ------------------------------------------------------------------ */
    /* Dedupe-aware creation */
    /* ------------------------------------------------------------------ */

    /**
     * Create a task unless an active row with the same dedupe_key already exists.
     *
     * Returns null on collision. This is the app-layer enforcement of the
     * "no duplicate active tasks for the same target" rule — see the migration
     * for why we don't use a partial unique index.
     *
     * @param  array<string, mixed>  $attributes
     */
    public static function tryCreateUnique(array $attributes): ?self
    {
        $dedupeKey = $attributes['dedupe_key'] ?? null;

        if ($dedupeKey === null) {
            /** @var self $task */
            $task = self::query()->create($attributes);

            return $task;
        }

        return self::query()->getConnection()->transaction(function () use ($attributes, $dedupeKey): ?self {
            $exists = self::query()
                ->where('dedupe_key', $dedupeKey)
                ->active()
                ->lockForUpdate()
                ->exists();

            if ($exists) {
                return null;
            }

            /** @var self $task */
            $task = self::query()->create($attributes);

            return $task;
        });
    }

    /* ------------------------------------------------------------------ */
    /* Helpers */
    /* ------------------------------------------------------------------ */

    public function durationSeconds(): ?int
    {
        if ($this->started_at === null) {
            return null;
        }

        $end = $this->finished_at ?? Carbon::now();

        return (int) $this->started_at->diffInSeconds($end);
    }
}
