<?php

declare(strict_types=1);

namespace App\Services\BackgroundTasks\Events;

enum TaskEventKind: string
{
    case Started = 'started';
    case Progress = 'progress';
    case Step = 'step';
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
}
