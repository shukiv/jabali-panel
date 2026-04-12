<?php

declare(strict_types=1);

namespace Tests\Feature\BackgroundTasks;

use App\Services\BackgroundTasks\Events\TaskEvent;
use App\Services\BackgroundTasks\Events\TaskEventKind;
use InvalidArgumentException;
use PHPUnit\Framework\TestCase;

class TaskEventTest extends TestCase
{
    public function test_parse_started_event(): void
    {
        $line = '{"ts":"2026-04-13T01:30:00Z","event":"started"}';
        $event = TaskEvent::fromJsonLine($line);

        $this->assertEquals(TaskEventKind::Started, $event->kind);
        $this->assertEquals('2026-04-13T01:30:00Z', $event->ts->format('Y-m-d\TH:i:s\Z'));
        $this->assertNull($event->percent);
        $this->assertNull($event->step);
    }

    public function test_parse_progress_event(): void
    {
        $line = '{"ts":"2026-04-13T01:35:00Z","event":"progress","percent":42,"step":"uploading"}';
        $event = TaskEvent::fromJsonLine($line);

        $this->assertEquals(TaskEventKind::Progress, $event->kind);
        $this->assertEquals(42, $event->percent);
        $this->assertEquals('uploading', $event->step);
    }

    public function test_parse_done_event_with_data(): void
    {
        $line = '{"ts":"2026-04-13T01:45:00Z","event":"done","data":{"bytes":1048576000}}';
        $event = TaskEvent::fromJsonLine($line);

        $this->assertEquals(TaskEventKind::Done, $event->kind);
        $this->assertEquals(['bytes' => 1048576000], $event->data);
    }

    public function test_parse_failed_event_with_error(): void
    {
        $line = '{"ts":"2026-04-13T01:50:00Z","event":"failed","error":"rsync: connection lost","step":"uploading"}';
        $event = TaskEvent::fromJsonLine($line);

        $this->assertEquals(TaskEventKind::Failed, $event->kind);
        $this->assertEquals('rsync: connection lost', $event->error);
        $this->assertEquals('uploading', $event->step);
    }

    public function test_invalid_json_throws(): void
    {
        $this->expectException(InvalidArgumentException::class);
        TaskEvent::fromJsonLine('{not valid json');
    }

    public function test_missing_required_field_throws(): void
    {
        $this->expectException(InvalidArgumentException::class);
        TaskEvent::fromJsonLine('{"event":"started"}'); // no ts
    }

    public function test_unknown_event_kind_throws(): void
    {
        $this->expectException(InvalidArgumentException::class);
        TaskEvent::fromJsonLine('{"ts":"2026-04-13T01:30:00Z","event":"exploded"}');
    }

    public function test_progress_out_of_range_throws(): void
    {
        $this->expectException(InvalidArgumentException::class);
        TaskEvent::fromJsonLine('{"ts":"2026-04-13T01:30:00Z","event":"progress","percent":150}');
    }

    public function test_round_trip_to_array(): void
    {
        $line = '{"ts":"2026-04-13T01:35:00Z","event":"progress","percent":42,"step":"uploading"}';
        $event = TaskEvent::fromJsonLine($line);
        $arr = $event->toArray();

        $this->assertEquals('progress', $arr['event']);
        $this->assertEquals(42, $arr['percent']);
        $this->assertEquals('uploading', $arr['step']);
    }

    public function test_terminal_events(): void
    {
        $this->assertFalse(TaskEventKind::Started->isTerminal());
        $this->assertFalse(TaskEventKind::Progress->isTerminal());
        $this->assertFalse(TaskEventKind::Step->isTerminal());
        $this->assertTrue(TaskEventKind::Done->isTerminal());
        $this->assertTrue(TaskEventKind::Failed->isTerminal());
        $this->assertTrue(TaskEventKind::Canceled->isTerminal());
    }
}
