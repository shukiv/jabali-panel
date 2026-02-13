<?php

declare(strict_types=1);

namespace App\Services;

use Carbon\CarbonImmutable;
use DateTimeZone;
use Illuminate\Support\Facades\Cache;
use Symfony\Component\Process\Process;

class SysstatMetrics
{
    /**
     * @return array{labels: array<int, string>, load: array<int, float>, iowait: array<int, float>, memory: array<int, float>, swap: array<int, float>}
     */
    public function history(int $points, int $intervalSeconds, string $labelFormat): array
    {
        if ($points <= 0 || $intervalSeconds <= 0) {
            return [];
        }

        $timezone = $this->systemTimezone();
        $end = CarbonImmutable::now($timezone);
        if ($intervalSeconds < 60) {
            $second = intdiv($end->second, $intervalSeconds) * $intervalSeconds;
            $end = $end->setTime($end->hour, $end->minute, $second);
        } else {
            $end = $end->second(0);
            if ($intervalSeconds >= 3600) {
                $end = $end->minute(0);
            }
            if ($intervalSeconds >= 86400) {
                $end = $end->hour(0)->minute(0);
            }
        }
        $start = $end->subSeconds(($points - 1) * $intervalSeconds);
        $endBucket = intdiv($end->getTimestamp(), $intervalSeconds);
        $cacheKey = sprintf('sysstat.history.%d.%d.%s.%d', $points, $intervalSeconds, $labelFormat, $endBucket);
        $ttl = $this->cacheTtl($intervalSeconds);

        return Cache::remember($cacheKey, $ttl, function () use ($start, $end, $points, $intervalSeconds, $labelFormat): array {
            $samples = $this->readSamples($start, $end, $this->coreOptions());
            if (empty($samples)) {
                return [];
            }

            return $this->resample($samples, $start, $points, $intervalSeconds, $labelFormat);
        });
    }

    /**
     * @return array{load1: float, load5: float, load15: float, iowait: float, memory: float, swap: float}|null
     */
    public function latest(): ?array
    {
        $timezone = $this->systemTimezone();
        $end = CarbonImmutable::now($timezone);
        $start = $end->subMinutes(15);
        $bucket = intdiv($end->getTimestamp(), 10);
        $cacheKey = sprintf('sysstat.latest.%d', $bucket);

        return Cache::remember($cacheKey, now()->addSeconds(10), function () use ($start, $end): ?array {
            $samples = $this->readSamples($start, $end, $this->coreOptions());
            if (empty($samples)) {
                return null;
            }

            $last = end($samples);
            if (! is_array($last)) {
                return null;
            }

            return [
                'load1' => (float) ($last['load1'] ?? 0),
                'load5' => (float) ($last['load5'] ?? 0),
                'load15' => (float) ($last['load15'] ?? 0),
                'iowait' => (float) ($last['iowait'] ?? 0),
                'memory' => (float) ($last['memory'] ?? 0),
                'swap' => (float) ($last['swap'] ?? 0),
            ];
        });
    }

    public function timezoneName(): string
    {
        return $this->systemTimezone()->getName();
    }

    /**
     * @return array<int, array{timestamp: int, load1: float, load5: float, load15: float, iowait: float, memory: float, swap: float}>
     */
    private function readSamples(CarbonImmutable $start, CarbonImmutable $end, array $options): array
    {
        $samples = [];
        $current = $start->startOfDay();
        $lastDay = $end->startOfDay();
        $startTimestamp = $start->getTimestamp();
        $endTimestamp = $end->getTimestamp();
        $useDailyCache = ($endTimestamp - $startTimestamp) > 21600;

        while ($current <= $lastDay) {
            $fileLong = sprintf('/var/log/sysstat/sa%s', $current->format('Ymd'));
            $fileShort = sprintf('/var/log/sysstat/sa%s', $current->format('d'));
            $file = is_readable($fileLong) ? $fileLong : $fileShort;
            if (! is_readable($file)) {
                $current = $current->addDay();

                continue;
            }

            $dayStart = $current->isSameDay($start) ? $start : $current->startOfDay();
            $dayEnd = $current->isSameDay($end) ? $end : $current->endOfDay();
            if ($useDailyCache) {
                $statistics = $this->readSadfCsvDay($file, $current);
            } else {
                $statistics = $this->readSadfCsv($file, $dayStart, $dayEnd, $options);
            }

            foreach ($statistics as $parsed) {
                if ($parsed['timestamp'] < $startTimestamp || $parsed['timestamp'] > $endTimestamp) {
                    continue;
                }
                $samples[] = $parsed;
            }

            $current = $current->addDay();
        }

        usort($samples, static fn (array $a, array $b): int => $a['timestamp'] <=> $b['timestamp']);

        return $samples;
    }

    /**
     * @return array<int, array<string, mixed>>
     */
    private function readSadf(string $file, CarbonImmutable $start, CarbonImmutable $end, array $options): array
    {
        $args = array_merge(
            [
                'sadf',
                '-j',
                '-T',
                $file,
                '--',
            ],
            $options,
            [
                '-s',
                $start->format('H:i:s'),
                '-e',
                $end->format('H:i:s'),
            ],
        );
        $process = new Process($args);
        $process->setTimeout(8);
        $process->setIdleTimeout(5);
        $process->run();

        $output = $this->cleanSadfOutput($process->getOutput());
        if ($output === null) {
            return [];
        }

        $payload = json_decode($output, true);
        if (! is_array($payload)) {
            return [];
        }

        $stats = $payload['sysstat']['hosts'][0]['statistics'] ?? [];
        if (! is_array($stats)) {
            return [];
        }

        return $stats;
    }

    /**
     * @return array<int, array{timestamp: int, load1: float, load5: float, load15: float, iowait: float, memory: float, swap: float}>
     */
    private function readSadfCsv(string $file, CarbonImmutable $start, CarbonImmutable $end, array $options): array
    {
        $datasets = [
            'queue' => $this->readSadfCsvDataset($file, $start, $end, ['-q']),
            'cpu' => $this->readSadfCsvDataset($file, $start, $end, ['-u']),
            'memory' => $this->readSadfCsvDataset($file, $start, $end, ['-r']),
            'swap' => $this->readSadfCsvDataset($file, $start, $end, ['-S']),
        ];

        $bucket = [];
        foreach ($datasets['queue'] as $row) {
            $timestamp = $row['timestamp'];
            $this->appendSample($bucket, $timestamp, [
                'load1' => $this->getCsvFloat($row, ['ldavg-1']) ?? 0.0,
                'load5' => $this->getCsvFloat($row, ['ldavg-5']) ?? 0.0,
                'load15' => $this->getCsvFloat($row, ['ldavg-15']) ?? 0.0,
            ]);
        }
        foreach ($datasets['cpu'] as $row) {
            $timestamp = $row['timestamp'];
            $this->appendSample($bucket, $timestamp, [
                'iowait' => $this->getCsvFloat($row, ['%iowait', 'iowait']) ?? 0.0,
            ]);
        }
        foreach ($datasets['memory'] as $row) {
            $timestamp = $row['timestamp'];
            $memory = $this->getCsvFloat($row, ['%memused', '%memused_percent', 'memused-percent', 'memused_percent']);
            $this->appendSample($bucket, $timestamp, [
                'memory' => $memory ?? 0.0,
            ]);
        }
        foreach ($datasets['swap'] as $row) {
            $timestamp = $row['timestamp'];
            $swap = $this->getCsvFloat($row, ['%swpused', 'swpused-percent', 'swpused_percent']);
            $this->appendSample($bucket, $timestamp, [
                'swap' => $swap ?? 0.0,
            ]);
        }

        return array_values($bucket);
    }

    /**
     * @return array<int, array{timestamp: int, load1: float, load5: float, load15: float, iowait: float, memory: float, swap: float}>
     */
    private function readSadfCsvDay(string $file, CarbonImmutable $day): array
    {
        $mtime = @filemtime($file) ?: 0;
        $cacheKey = sprintf('sysstat.sadf.csv.day.%s.%d', md5($file), $mtime);

        return Cache::remember($cacheKey, now()->addMinutes(10), function () use ($file, $day): array {
            $dayStart = $day->startOfDay();
            $dayEnd = $day->endOfDay();

            return $this->readSadfCsv($file, $dayStart, $dayEnd, $this->coreOptions());
        });
    }

    /**
     * @return array<int, array<string, mixed>>
     */
    private function readSadfCsvDataset(string $file, CarbonImmutable $start, CarbonImmutable $end, array $options): array
    {
        $args = array_merge(
            [
                'sadf',
                '-d',
                $file,
                '--',
            ],
            $options,
        );
        $process = new Process($args);
        $process->setTimeout(8);
        $process->setIdleTimeout(5);
        $process->run();

        $output = $process->getOutput();
        if ($output === '') {
            return [];
        }

        $lines = preg_split('/\\r\\n|\\r|\\n/', trim($output)) ?: [];
        $headers = [];
        $rows = [];

        foreach ($lines as $line) {
            $line = trim($line);
            if ($line === '') {
                continue;
            }
            if (str_starts_with($line, '#')) {
                $headerLine = ltrim($line, "# \t");
                $headers = str_getcsv($headerLine, ';');
                continue;
            }
            if ($headers === []) {
                continue;
            }
            $values = str_getcsv($line, ';');
            if (count($values) < count($headers)) {
                continue;
            }
            $row = array_combine($headers, array_slice($values, 0, count($headers)));
            if (! is_array($row)) {
                continue;
            }
            $timestampValue = $this->parseCsvTimestamp($row['timestamp'] ?? null);
            if ($timestampValue === null) {
                continue;
            }
            $row['timestamp'] = $timestampValue;
            if ($timestampValue < $start->getTimestamp() || $timestampValue > $end->getTimestamp()) {
                continue;
            }
            $rows[] = $row;
        }

        return $rows;
    }

    /**
     * @param  array<int, array{timestamp: int, load1: float, load5: float, load15: float, iowait: float, memory: float, swap: float}>  $bucket
     */
    private function appendSample(array &$bucket, int $timestamp, array $values): void
    {
        if (! isset($bucket[$timestamp])) {
            $bucket[$timestamp] = [
                'timestamp' => $timestamp,
                'load1' => 0.0,
                'load5' => 0.0,
                'load15' => 0.0,
                'iowait' => 0.0,
                'memory' => 0.0,
                'swap' => 0.0,
            ];
        }

        foreach ($values as $key => $value) {
            if ($value !== null) {
                $bucket[$timestamp][$key] = (float) $value;
            }
        }
    }

    private function parseCsvTimestamp(string|null $value): ?int
    {
        if ($value === null || $value === '') {
            return null;
        }

        try {
            return CarbonImmutable::parse($value, $this->systemTimezone())->getTimestamp();
        } catch (\Throwable) {
            return null;
        }
    }

    private function getCsvFloat(array $row, array $keys): ?float
    {
        foreach ($keys as $key) {
            if (! array_key_exists($key, $row)) {
                continue;
            }
            $value = str_replace(',', '.', (string) $row[$key]);
            if ($value === '') {
                continue;
            }
            if (is_numeric($value)) {
                return (float) $value;
            }
        }

        return null;
    }

    private function cleanSadfOutput(string $output): ?string
    {
        $start = strpos($output, '{');
        if ($start === false) {
            return null;
        }

        $trimmed = substr($output, $start);
        $trimmed = ltrim($trimmed);

        return $trimmed === '' ? null : $trimmed;
    }

    /**
     * @return array<int, array<string, mixed>>
     */
    private function readSadfDay(string $file, CarbonImmutable $day, array $options): array
    {
        $mtime = @filemtime($file) ?: 0;
        $optionsKey = implode(',', $options);
        $cacheKey = sprintf('sysstat.sadf.day.%s.%d.%s', md5($file), $mtime, md5($optionsKey));

        return Cache::remember($cacheKey, now()->addMinutes(10), function () use ($file, $day, $options): array {
            $dayStart = $day->startOfDay();
            $dayEnd = $day->endOfDay();

            return $this->readSadf($file, $dayStart, $dayEnd, $options);
        });
    }

    /**
     * @return array<int, string>
     */
    private function coreOptions(): array
    {
        return ['-q', '-u', '-r', '-S'];
    }

    private function cacheTtl(int $intervalSeconds): \DateInterval|\DateTimeInterface|int
    {
        if ($intervalSeconds <= 10) {
            return 10;
        }

        return max(30, min(300, $intervalSeconds));
    }

    /**
     * @param  array<string, mixed>  $stat
     * @return array{timestamp: int, load1: float, load5: float, load15: float, iowait: float, memory: float, swap: float}|null
     */
    private function parseSample(array $stat): ?array
    {
        $timestamp = $this->parseTimestamp($stat['timestamp'] ?? []);
        if ($timestamp === null) {
            return null;
        }

        $queue = $stat['queue'] ?? [];
        $load1 = $this->getFloat($queue, ['ldavg-1', 'ldavg_1']) ?? 0.0;
        $load5 = $this->getFloat($queue, ['ldavg-5', 'ldavg_5']) ?? 0.0;
        $load15 = $this->getFloat($queue, ['ldavg-15', 'ldavg_15']) ?? 0.0;

        $cpuLoad = $stat['cpu-load'] ?? $stat['cpu-load-all'] ?? [];
        $iowait = $this->extractCpuMetric($cpuLoad, 'iowait');

        $memory = $stat['memory'] ?? [];
        $memPercent = $this->getFloat($memory, ['memused-percent', 'memused_percent', 'memused']);
        if ($memPercent === null) {
            $memPercent = $this->percentFromTotals($memory, 'kbmemused', 'kbmemfree', 'kbmemtotal');
        }

        $swap = $stat['swap'] ?? $stat['memory'] ?? [];
        $swapPercent = $this->getFloat($swap, ['swpused-percent', 'swpused_percent', 'swpused']);
        if ($swapPercent === null) {
            $swapPercent = $this->percentFromTotals($swap, 'kbswpused', 'kbswpfree', 'kbswptotal');
        }

        return [
            'timestamp' => $timestamp->getTimestamp(),
            'load1' => (float) $load1,
            'load5' => (float) $load5,
            'load15' => (float) $load15,
            'iowait' => (float) ($iowait ?? 0.0),
            'memory' => (float) ($memPercent ?? 0.0),
            'swap' => (float) ($swapPercent ?? 0.0),
        ];
    }

    /**
     * @return array{labels: array<int, string>, load: array<int, float>, iowait: array<int, float>, memory: array<int, float>, swap: array<int, float>}
     */
    private function resample(array $samples, CarbonImmutable $start, int $points, int $intervalSeconds, string $labelFormat): array
    {
        $labels = [];
        $loadSeries = [];
        $ioWaitSeries = [];
        $memorySeries = [];
        $swapSeries = [];

        $index = 0;
        $current = null;
        $first = $samples[0] ?? null;
        $count = count($samples);

        for ($i = 0; $i < $points; $i++) {
            $bucketTime = $start->addSeconds($i * $intervalSeconds);
            while ($index < $count && $samples[$index]['timestamp'] <= $bucketTime->getTimestamp()) {
                $current = $samples[$index];
                $index++;
            }

            $sample = $current ?? $first;
            $labels[] = $bucketTime->format($labelFormat);
            $loadSeries[] = $sample ? round((float) $sample['load1'], 3) : 0.0;
            $ioWaitSeries[] = $sample ? round((float) $sample['iowait'], 2) : 0.0;
            $memorySeries[] = $sample ? round((float) $sample['memory'], 1) : 0.0;
            $swapSeries[] = $sample ? round((float) $sample['swap'], 1) : 0.0;
        }

        return [
            'labels' => $labels,
            'load' => $loadSeries,
            'iowait' => $ioWaitSeries,
            'memory' => $memorySeries,
            'swap' => $swapSeries,
        ];
    }

    /**
     * @param  array<string, mixed>  $timestamp
     */
    private function parseTimestamp(array $timestamp): ?CarbonImmutable
    {
        $date = $timestamp['date'] ?? null;
        $time = $timestamp['time'] ?? null;
        if (! is_string($date) || ! is_string($time)) {
            return null;
        }

        $value = CarbonImmutable::createFromFormat('Y-m-d H:i:s', $date.' '.$time, $this->systemTimezone());
        if ($value === false) {
            return null;
        }

        return $value;
    }

    private function systemTimezone(): DateTimeZone
    {
        static $timezone = null;

        if ($timezone instanceof DateTimeZone) {
            return $timezone;
        }

        $name = getenv('TZ') ?: null;
        if (! $name && is_file('/etc/timezone')) {
            $name = trim((string) file_get_contents('/etc/timezone'));
        }
        if (! $name && is_link('/etc/localtime')) {
            $target = readlink('/etc/localtime');
            if (is_string($target) && str_contains($target, '/zoneinfo/')) {
                $name = substr($target, strpos($target, '/zoneinfo/') + 10);
            }
        }
        if (! $name) {
            $name = config('app.timezone') ?: date_default_timezone_get();
        }

        try {
            $timezone = new DateTimeZone($name);
        } catch (\Exception $e) {
            $timezone = new DateTimeZone('UTC');
        }

        return $timezone;
    }

    /**
     * @param  array<string, mixed>  $source
     */
    private function getFloat(array $source, array $keys): ?float
    {
        foreach ($keys as $key) {
            if (array_key_exists($key, $source) && is_numeric($source[$key])) {
                return (float) $source[$key];
            }
        }

        return null;
    }

    /**
     * @param  array<string, mixed>  $source
     */
    private function extractCpuMetric(mixed $source, string $metric): ?float
    {
        if (is_array($source) && array_key_exists($metric, $source) && is_numeric($source[$metric])) {
            return (float) $source[$metric];
        }

        if (is_array($source)) {
            $entries = $source;
            if (array_values($entries) === $entries) {
                foreach ($entries as $entry) {
                    if (! is_array($entry)) {
                        continue;
                    }
                    $cpu = $entry['cpu'] ?? $entry['cpu-load'] ?? null;
                    if ($cpu === 'all' || $cpu === 0 || $cpu === '0') {
                        if (isset($entry[$metric]) && is_numeric($entry[$metric])) {
                            return (float) $entry[$metric];
                        }
                    }
                }
                foreach ($entries as $entry) {
                    if (is_array($entry) && isset($entry[$metric]) && is_numeric($entry[$metric])) {
                        return (float) $entry[$metric];
                    }
                }
            }
        }

        return null;
    }

    /**
     * @param  array<string, mixed>  $source
     */
    private function percentFromTotals(array $source, string $usedKey, string $freeKey, string $totalKey): ?float
    {
        $used = $source[$usedKey] ?? null;
        $free = $source[$freeKey] ?? null;
        $total = $source[$totalKey] ?? null;

        if (is_numeric($total) && (float) $total > 0) {
            $usedValue = is_numeric($used) ? (float) $used : null;
            if ($usedValue === null && is_numeric($free)) {
                $usedValue = (float) $total - (float) $free;
            }
            if ($usedValue !== null) {
                return round(($usedValue / (float) $total) * 100, 2);
            }
        }

        return null;
    }
}
