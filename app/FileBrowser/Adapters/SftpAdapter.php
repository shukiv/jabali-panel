<?php

declare(strict_types=1);

namespace App\FileBrowser\Adapters;

use App\FileBrowser\Support\PathSanitizer;
use RuntimeException;

class SftpAdapter implements FileBrowserAdapter, FileOperations
{
    protected object $filesystem;

    protected string $root;

    public static function fromConfig(array $config): static
    {
        return new static($config);
    }

    public function __construct(array $config)
    {
        if (! class_exists(\League\Flysystem\PhpseclibV3\SftpAdapter::class)) {
            throw new RuntimeException(
                'The league/flysystem-sftp-v3 package is required for the SFTP adapter. '
                .'Install it with: composer require league/flysystem-sftp-v3'
            );
        }

        $this->root = rtrim($config['root'] ?? '/', '/');

        $connectionProvider = new \League\Flysystem\PhpseclibV3\SftpConnectionProvider(
            host: $config['host'] ?? '',
            username: $config['username'] ?? '',
            password: $config['password'] ?? null,
            privateKey: $config['private_key'] ?? null,
            passphrase: $config['passphrase'] ?? null,
            port: (int) ($config['port'] ?? 22),
            timeout: (int) ($config['timeout'] ?? 30),
        );

        $adapter = new \League\Flysystem\PhpseclibV3\SftpAdapter(
            $connectionProvider,
            $this->root,
        );

        $this->filesystem = new \League\Flysystem\Filesystem($adapter);
    }

    public function files(): FileOperations
    {
        return $this;
    }

    public function archiver(): ?Archiver
    {
        return null;
    }

    public function permissions(): ?PermissionManager
    {
        return null;
    }

    public function list(string $path, bool $showHidden = false): array
    {
        $path = PathSanitizer::clean($path);

        $listing = $this->filesystem->listContents($path, false);
        $items = [];

        foreach ($listing as $item) {
            $name = basename($item->path());

            if (! $showHidden && str_starts_with($name, '.')) {
                continue;
            }

            $isDir = $item->isDir();
            $size = null;
            $modified = 0;

            if (! $isDir && method_exists($item, 'fileSize')) {
                try {
                    $size = $item->fileSize();
                } catch (\Throwable) {
                    $size = null;
                }
            }

            if (method_exists($item, 'lastModified')) {
                try {
                    $modified = $item->lastModified();
                } catch (\Throwable) {
                    $modified = 0;
                }
            }

            $items[] = [
                'name' => $name,
                'path' => $item->path(),
                'is_dir' => $isDir,
                'size' => $size,
                'modified' => $modified,
                'permissions' => '',
            ];
        }

        return ['items' => $items];
    }

    public function read(string $path): array
    {
        $path = PathSanitizer::clean($path);
        $content = $this->filesystem->read($path);

        return ['content' => base64_encode($content)];
    }

    public function write(string $path, string $content): array
    {
        $path = PathSanitizer::clean($path);
        $this->filesystem->write($path, $content);

        return ['success' => true];
    }

    public function delete(string $path): array
    {
        $path = PathSanitizer::clean($path);

        try {
            $this->filesystem->delete($path);
        } catch (\Throwable) {
            $this->filesystem->deleteDirectory($path);
        }

        return ['success' => true];
    }

    public function mkdir(string $path): array
    {
        $path = PathSanitizer::clean($path);
        $this->filesystem->createDirectory($path);

        return ['success' => true];
    }

    public function rename(string $oldPath, string $newPath): array
    {
        $oldPath = PathSanitizer::clean($oldPath);
        $newPath = PathSanitizer::clean($newPath);
        $this->filesystem->move($oldPath, $newPath);

        return ['success' => true];
    }

    public function copy(string $source, string $destination): array
    {
        $source = PathSanitizer::clean($source);
        $destination = PathSanitizer::clean($destination);
        $this->filesystem->copy($source, $destination);

        return ['success' => true];
    }

    public function move(string $source, string $destination): array
    {
        $source = PathSanitizer::clean($source);
        $destination = PathSanitizer::clean($destination);
        $this->filesystem->move($source, $destination);

        return ['success' => true];
    }

    public function upload(string $directory, string $filename, string $content): array
    {
        $directory = PathSanitizer::clean($directory);
        $sanitized = PathSanitizer::filename($filename);

        $path = rtrim($directory, '/').'/'.$sanitized;
        $this->filesystem->write($path, $content);

        return ['success' => true, 'path' => $path];
    }

    public function info(string $path): array
    {
        $path = PathSanitizer::clean($path);

        $mimeType = '';
        try {
            $mimeType = $this->filesystem->mimeType($path);
        } catch (\Throwable) {
            // Not all files have detectable mime types
        }

        return [
            'info' => [
                'permissions' => '',
                'mime_type' => $mimeType,
            ],
        ];
    }
}
