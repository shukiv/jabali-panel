<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

class ServerProcess extends Model
{
    protected static ?string $latestBatchIdCache = null;

    protected $fillable = [
        'batch_id',
        'rank',
        'pid',
        'user',
        'command',
        'cpu',
        'memory',
        'captured_at',
    ];

    protected function casts(): array
    {

        return [
            'cpu' => 'decimal:2',
            'memory' => 'decimal:2',
            'captured_at' => 'datetime',
        ];

    }

    public function scopeLatestBatch($query)
    {
        if (static::$latestBatchIdCache === null) {
            static::$latestBatchIdCache = static::orderBy('captured_at', 'desc')->value('batch_id');
        }

        return $query->where('batch_id', static::$latestBatchIdCache);
    }

    public static function captureProcesses(array $processes, int $total = 0): string
    {
        $batchId = (string) \Illuminate\Support\Str::uuid();
        $now = now();

        // Delete old snapshots (keep last 10 batches for history)
        $keepBatches = static::select('batch_id')
            ->distinct()
            ->orderBy('captured_at', 'desc')
            ->limit(10)
            ->pluck('batch_id');

        if ($keepBatches->isNotEmpty()) {
            static::whereNotIn('batch_id', $keepBatches)->delete();
        }

        static::$latestBatchIdCache = $batchId;

        // Insert new processes
        $rows = [];
        foreach ($processes as $index => $proc) {
            $rows[] = [
                'batch_id' => $batchId,
                'rank' => $index + 1,
                'pid' => $proc['pid'] ?? 0,
                'user' => $proc['user'] ?? 'unknown',
                'command' => $proc['command'] ?? '',
                'cpu' => $proc['cpu'] ?? 0,
                'memory' => $proc['memory'] ?? 0,
                'captured_at' => $now,
            ];
        }

        if ($rows) {
            static::insert($rows);
        }

        return $batchId;
    }
}
