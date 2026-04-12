<?php

declare(strict_types=1);

namespace App\Services\BackgroundTasks\Events;

use DateTimeImmutable;
use InvalidArgumentException;
use JsonException;

/**
 * Immutable value object representing a single JSONL event line emitted by a
 * spawned background task binary.
 *
 * Wire format (one line per event):
 *   {"ts":"2026-04-13T01:30:00Z","event":"started"}
 *   {"ts":"2026-04-13T01:35:00Z","event":"progress","percent":42,"step":"uploading"}
 *   {"ts":"2026-04-13T01:45:00Z","event":"done","data":{"bytes":1048576000}}
 *   {"ts":"2026-04-13T01:50:00Z","event":"failed","error":"rsync: connection lost"}
 */
final class TaskEvent
{
    public function __construct(
        public readonly TaskEventKind $kind,
        public readonly DateTimeImmutable $ts,
        public readonly ?int $percent = null,
        public readonly ?string $step = null,
        public readonly ?string $error = null,
        /** @var array<string, mixed>|null */
        public readonly ?array $data = null,
    ) {}

    /**
     * Parse a single JSONL line.
     *
     * @throws InvalidArgumentException on malformed input, unknown event kind,
     *                                  missing required field, or out-of-range
     *                                  values.
     */
    public static function fromJsonLine(string $line): self
    {
        $line = trim($line);
        if ($line === '') {
            throw new InvalidArgumentException('Empty event line');
        }

        try {
            /** @var array<string, mixed> $data */
            $data = json_decode($line, associative: true, flags: JSON_THROW_ON_ERROR);
        } catch (JsonException $e) {
            throw new InvalidArgumentException('Invalid JSON: '.$e->getMessage(), previous: $e);
        }

        if (! is_array($data)) {
            throw new InvalidArgumentException('Event line must be a JSON object');
        }

        if (! isset($data['ts']) || ! is_string($data['ts'])) {
            throw new InvalidArgumentException('Event missing required field: ts');
        }

        if (! isset($data['event']) || ! is_string($data['event'])) {
            throw new InvalidArgumentException('Event missing required field: event');
        }

        $kind = TaskEventKind::tryFrom($data['event']);
        if ($kind === null) {
            throw new InvalidArgumentException("Unknown event kind: {$data['event']}");
        }

        try {
            $ts = new DateTimeImmutable($data['ts']);
        } catch (\Throwable $e) {
            throw new InvalidArgumentException("Invalid timestamp: {$data['ts']}", previous: $e);
        }

        $percent = null;
        if (isset($data['percent'])) {
            if (! is_int($data['percent']) || $data['percent'] < 0 || $data['percent'] > 100) {
                throw new InvalidArgumentException('percent must be an integer in [0,100]');
            }
            $percent = $data['percent'];
        }

        $step = isset($data['step']) && is_string($data['step']) ? $data['step'] : null;
        $error = isset($data['error']) && is_string($data['error']) ? $data['error'] : null;
        $extra = isset($data['data']) && is_array($data['data']) ? $data['data'] : null;

        return new self(
            kind: $kind,
            ts: $ts,
            percent: $percent,
            step: $step,
            error: $error,
            data: $extra,
        );
    }

    /**
     * @return array<string, mixed>
     */
    public function toArray(): array
    {
        $out = [
            'ts' => $this->ts->format('Y-m-d\TH:i:s\Z'),
            'event' => $this->kind->value,
        ];

        if ($this->percent !== null) {
            $out['percent'] = $this->percent;
        }
        if ($this->step !== null) {
            $out['step'] = $this->step;
        }
        if ($this->error !== null) {
            $out['error'] = $this->error;
        }
        if ($this->data !== null) {
            $out['data'] = $this->data;
        }

        return $out;
    }

    public function toJsonLine(): string
    {
        return json_encode($this->toArray(), JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE)."\n";
    }
}
