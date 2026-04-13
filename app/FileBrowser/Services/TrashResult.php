<?php

declare(strict_types=1);

namespace App\FileBrowser\Services;

class TrashResult
{
    /**
     * @param  array<string, mixed>  $data
     */
    public function __construct(
        public readonly array $data = [],
    ) {}

    public function restoredPath(): string
    {
        return $this->data['restored_path'] ?? '';
    }

    public function deletedCount(): int
    {
        return $this->data['deleted'] ?? 0;
    }
}
