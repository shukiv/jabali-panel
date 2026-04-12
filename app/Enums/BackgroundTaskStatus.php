<?php

declare(strict_types=1);

namespace App\Enums;

enum BackgroundTaskStatus: string
{
    case Pending = 'pending';
    case Running = 'running';
    case Done = 'done';
    case Failed = 'failed';
    case Canceled = 'canceled';

    public function isTerminal(): bool
    {
        return match ($this) {
            self::Done, self::Failed, self::Canceled => true,
            default => false,
        };
    }

    public function isActive(): bool
    {
        return ! $this->isTerminal();
    }

    public function label(): string
    {
        return match ($this) {
            self::Pending => __('Pending'),
            self::Running => __('Running'),
            self::Done => __('Done'),
            self::Failed => __('Failed'),
            self::Canceled => __('Canceled'),
        };
    }

    public function color(): string
    {
        return match ($this) {
            self::Pending => 'gray',
            self::Running => 'info',
            self::Done => 'success',
            self::Failed => 'danger',
            self::Canceled => 'warning',
        };
    }
}
