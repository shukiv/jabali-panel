<?php

declare(strict_types=1);

namespace App\FileBrowser\Services;

use App\FileBrowser\Adapters\FileBrowserAdapter;
use RuntimeException;

class TrashManager
{
    private const DIR = '.trash';

    private TrashStore $store;

    public function __construct(
        private readonly FileBrowserAdapter $adapter,
        ?TrashStore $store = null,
    ) {
        $this->store = $store ?? new SidecarTrashStore($adapter);
    }

    public function trash(string $path): TrashResult
    {
        if ($path === '' || $path === '.' || $path === '..') {
            throw new RuntimeException('Invalid path.');
        }

        $basename = basename($path);
        $timestamp = time();
        $trashName = $timestamp.'_'.$basename;

        $this->ensureDir();

        $this->store->recordTrash($trashName, [
            'original_path' => $path,
            'trashed_at' => $timestamp,
            'name' => $basename,
        ]);

        try {
            $this->adapter->files()->move($path, self::DIR.'/'.$trashName);
        } catch (RuntimeException $e) {
            $this->store->forgetTrash($trashName);
            throw new RuntimeException('Failed to move to trash: '.$e->getMessage());
        }

        return new TrashResult;
    }

    /**
     * @return list<array{trash_name: string, name: string, original_path: string, trashed_at: int, is_dir: bool, size: int|null}>
     */
    public function items(): array
    {
        try {
            $result = $this->adapter->files()->list(self::DIR, showHidden: false);
        } catch (RuntimeException) {
            return [];
        }

        $items = [];
        foreach ($result['items'] ?? [] as $entry) {
            $name = $entry['name'] ?? '';
            if (str_ends_with($name, '.json')) {
                continue;
            }

            $meta = $this->store->readMeta($name);

            $items[] = [
                'trash_name' => $name,
                'name' => $meta['name'] ?? $name,
                'original_path' => $meta['original_path'] ?? '',
                'trashed_at' => $meta['trashed_at'] ?? ($entry['modified'] ?? 0),
                'is_dir' => $entry['is_dir'] ?? false,
                'size' => ($entry['is_dir'] ?? false) ? null : ($entry['size'] ?? null),
            ];
        }

        return $items;
    }

    public function restore(string $trashName): TrashResult
    {
        $trashName = basename($trashName);
        $this->guardTrashName($trashName);

        $meta = $this->store->readMeta($trashName);
        if ($meta === [] || ! isset($meta['original_path'])) {
            throw new RuntimeException('Trash metadata not found.');
        }

        $originalPath = $meta['original_path'];
        if (str_contains($originalPath, '..') || str_starts_with($originalPath, '/')) {
            throw new RuntimeException('Invalid restore path in trash metadata.');
        }

        $this->adapter->files()->move(self::DIR.'/'.$trashName, $originalPath);
        $this->store->forgetTrash($trashName);

        return new TrashResult(['restored_path' => $originalPath]);
    }

    public function deletePermanently(string $trashName): TrashResult
    {
        $trashName = basename($trashName);
        $this->guardTrashName($trashName);

        $this->adapter->files()->delete(self::DIR.'/'.$trashName);
        $this->store->forgetTrash($trashName);

        return new TrashResult;
    }

    public function empty(): TrashResult
    {
        $items = $this->items();
        $deleted = 0;

        foreach ($items as $item) {
            try {
                $this->adapter->files()->delete(self::DIR.'/'.$item['trash_name']);
                $this->store->forgetTrash($item['trash_name']);
                $deleted++;
            } catch (RuntimeException) {
                // continue
            }
        }

        return new TrashResult(['deleted' => $deleted]);
    }

    private function ensureDir(): void
    {
        try {
            $this->adapter->files()->list(self::DIR);
        } catch (RuntimeException) {
            $this->adapter->files()->mkdir(self::DIR);
        }
    }

    private function guardTrashName(string $name): void
    {
        if ($name === '' || $name === '.' || $name === '..'
            || str_contains($name, '/') || str_contains($name, '\\')) {
            throw new RuntimeException('Invalid trash name.');
        }
    }
}
