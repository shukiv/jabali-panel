<?php

declare(strict_types=1);

namespace App\FileBrowser\Adapters;

use App\FileBrowser\Support\PathSanitizer;
use RuntimeException;

class LocalAdapter implements Archiver, FileBrowserAdapter, FileOperations, PermissionManager
{
    protected string $rootPath;

    private PathSanitizer $paths;

    /** @param array{root: string} $config */
    public static function fromConfig(array $config): static
    {
        return new static($config['root']);
    }

    /** @return array{root: string} */
    public function toArray(): array
    {
        return ['root' => $this->rootPath];
    }

    public function __construct(string $rootPath)
    {
        $this->rootPath = rtrim($rootPath, '/');

        if ($this->rootPath === '') {
            throw new RuntimeException('LocalAdapter requires a non-empty root path.');
        }

        if (! is_dir($this->rootPath)) {
            throw new RuntimeException("Root path does not exist: {$this->rootPath}");
        }

        $this->paths = PathSanitizer::local($this->rootPath);
    }

    public function files(): FileOperations
    {
        return $this;
    }

    public function archiver(): ?Archiver
    {
        return $this;
    }

    public function permissions(): ?PermissionManager
    {
        return $this;
    }

    public function list(string $path, bool $showHidden = false): array
    {
        $fullPath = $this->paths->resolve($path);

        if (! is_dir($fullPath)) {
            throw new RuntimeException('Directory not found.');
        }

        $entries = scandir($fullPath);
        if ($entries === false) {
            throw new RuntimeException('Failed to read directory.');
        }

        $items = [];

        foreach ($entries as $entry) {
            if ($entry === '.' || $entry === '..') {
                continue;
            }

            if (! $showHidden && str_starts_with($entry, '.')) {
                continue;
            }

            $entryPath = $fullPath.'/'.$entry;
            $relativePath = $this->relativePath($entryPath);

            $isDir = is_dir($entryPath);
            $size = $isDir ? null : (int) filesize($entryPath);
            $modified = (int) filemtime($entryPath);
            $perms = fileperms($entryPath);
            $permissions = $perms !== false ? decoct($perms & 0777) : '0000';

            $items[] = [
                'name' => $entry,
                'path' => $relativePath,
                'is_dir' => $isDir,
                'size' => $size,
                'modified' => $modified,
                'permissions' => $permissions,
            ];
        }

        return ['items' => $items];
    }

    public function read(string $path): array
    {
        $fullPath = $this->paths->resolve($path);

        if (! is_file($fullPath)) {
            throw new RuntimeException('File not found.');
        }

        $content = file_get_contents($fullPath);

        if ($content === false) {
            throw new RuntimeException('Failed to read file.');
        }

        return ['content' => base64_encode($content)];
    }

    public function write(string $path, string $content): array
    {
        $fullPath = $this->paths->resolve($path, allowNew: true);

        $dir = dirname($fullPath);
        if (! is_dir($dir)) {
            throw new RuntimeException('Parent directory does not exist.');
        }

        $result = file_put_contents($fullPath, $content);

        if ($result === false) {
            throw new RuntimeException('Failed to write file.');
        }

        return ['success' => true];
    }

    public function delete(string $path): array
    {
        $fullPath = $this->paths->resolve($path);

        if (! file_exists($fullPath)) {
            throw new RuntimeException('File or directory not found.');
        }

        if (is_dir($fullPath)) {
            $this->deleteDirectory($fullPath);
        } else {
            if (! unlink($fullPath)) {
                throw new RuntimeException('Failed to delete file.');
            }
        }

        return ['success' => true];
    }

    public function mkdir(string $path): array
    {
        $fullPath = $this->paths->resolve($path, allowNew: true);

        if (file_exists($fullPath)) {
            throw new RuntimeException('Path already exists.');
        }

        if (! mkdir($fullPath, 0755, true)) {
            throw new RuntimeException('Failed to create directory.');
        }

        return ['success' => true];
    }

    public function rename(string $oldPath, string $newPath): array
    {
        $fullOld = $this->paths->resolve($oldPath);
        $fullNew = $this->paths->resolve($newPath, allowNew: true);

        if (! file_exists($fullOld)) {
            throw new RuntimeException('Source not found.');
        }

        if (file_exists($fullNew)) {
            throw new RuntimeException('Destination already exists.');
        }

        if (! rename($fullOld, $fullNew)) {
            throw new RuntimeException('Failed to rename.');
        }

        return ['success' => true];
    }

    public function copy(string $source, string $destination): array
    {
        $fullSource = $this->paths->resolve($source);
        $fullDest = $this->paths->resolve($destination, allowNew: true);

        if (! file_exists($fullSource)) {
            throw new RuntimeException('Source not found.');
        }

        if (is_dir($fullSource)) {
            $this->copyDirectory($fullSource, $fullDest);
        } else {
            $destDir = dirname($fullDest);
            if (! is_dir($destDir)) {
                mkdir($destDir, 0755, true);
            }
            if (! copy($fullSource, $fullDest)) {
                throw new RuntimeException('Failed to copy file.');
            }
        }

        return ['success' => true];
    }

    public function move(string $source, string $destination): array
    {
        $fullSource = $this->paths->resolve($source);
        $fullDest = $this->paths->resolve($destination, allowNew: true);

        if (! file_exists($fullSource)) {
            throw new RuntimeException('Source not found.');
        }

        $destDir = dirname($fullDest);
        if (! is_dir($destDir)) {
            mkdir($destDir, 0755, true);
        }

        if (! rename($fullSource, $fullDest)) {
            throw new RuntimeException('Failed to move.');
        }

        return ['success' => true];
    }

    public function upload(string $directory, string $filename, string $content): array
    {
        $dirPath = $this->paths->resolve($directory);

        if (! is_dir($dirPath)) {
            throw new RuntimeException('Upload directory not found.');
        }

        $sanitized = PathSanitizer::filename($filename);
        $fullPath = $dirPath.'/'.$sanitized;

        // Verify the target is still within root
        $realDir = realpath($dirPath);
        $realRoot = realpath($this->rootPath);
        if ($realDir === false || $realRoot === false || ! str_starts_with($realDir, $realRoot)) {
            throw new RuntimeException('Invalid upload directory.');
        }

        $result = file_put_contents($fullPath, $content);

        if ($result === false) {
            throw new RuntimeException('Failed to upload file.');
        }

        return ['success' => true, 'path' => $this->relativePath($fullPath)];
    }

    public function extract(string $path): array
    {
        $fullPath = $this->paths->resolve($path);

        if (! is_file($fullPath)) {
            throw new RuntimeException('Archive not found.');
        }

        $dir = dirname($fullPath);
        $extension = strtolower(pathinfo($fullPath, PATHINFO_EXTENSION));
        $basename = pathinfo($fullPath, PATHINFO_FILENAME);

        if ($extension === 'zip') {
            $zip = new \ZipArchive;
            if ($zip->open($fullPath) !== true) {
                throw new RuntimeException('Failed to open zip archive.');
            }

            // Zip Slip prevention: validate all entry names before extraction
            $extractDir = $dir.'/'.$basename;
            $realExtractDir = null;
            if (! is_dir($extractDir)) {
                mkdir($extractDir, 0755, true);
            }
            $realExtractDir = realpath($extractDir);

            for ($i = 0; $i < $zip->numFiles; $i++) {
                $entryName = $zip->getNameIndex($i);
                if ($entryName === false) {
                    continue;
                }
                // Block entries with path traversal
                if (str_contains($entryName, '..')) {
                    $zip->close();
                    throw new RuntimeException('Archive contains unsafe path: '.$entryName);
                }
                // Verify resolved path stays within extract directory
                $entryPath = $realExtractDir.'/'.$entryName;
                $resolvedEntry = realpath(dirname($entryPath));
                if ($resolvedEntry !== false && ! str_starts_with($resolvedEntry, $realExtractDir)) {
                    $zip->close();
                    throw new RuntimeException('Archive entry escapes target directory.');
                }
            }

            $zip->extractTo($extractDir);
            $zip->close();
        } elseif ($extension === 'gz' || $extension === 'bz2' || $extension === 'tar') {
            try {
                $phar = new \PharData($fullPath);
                $extractDir = $dir.'/'.$basename;

                // For .tar.gz or .tar.bz2, the basename still has .tar
                if (str_ends_with(strtolower($basename), '.tar')) {
                    $extractDir = $dir.'/'.pathinfo($basename, PATHINFO_FILENAME);
                }

                if (! is_dir($extractDir)) {
                    mkdir($extractDir, 0755, true);
                }

                // Zip Slip prevention for tar archives
                $realExtractDir = realpath($extractDir);
                foreach ($phar as $entry) {
                    $entryPath = $entry->getPathname();
                    if (str_contains($entryPath, '..')) {
                        throw new RuntimeException('Archive contains unsafe path.');
                    }
                }

                $phar->extractTo($extractDir, null, true);
            } catch (RuntimeException $e) {
                throw $e;
            } catch (\Throwable $e) {
                throw new RuntimeException('Failed to extract archive: '.$e->getMessage());
            }
        } else {
            throw new RuntimeException('Unsupported archive format.');
        }

        return ['success' => true];
    }

    public function chmod(string $path, string $mode): array
    {
        $fullPath = $this->paths->resolve($path);

        if (! file_exists($fullPath)) {
            throw new RuntimeException('File or directory not found.');
        }

        if (! preg_match('/^[0-7]{3,4}$/', $mode)) {
            throw new RuntimeException('Invalid permission mode.');
        }

        if (! chmod($fullPath, (int) octdec($mode))) {
            throw new RuntimeException('Failed to change permissions.');
        }

        return ['success' => true];
    }

    public function info(string $path): array
    {
        $fullPath = $this->paths->resolve($path);

        if (! file_exists($fullPath)) {
            throw new RuntimeException('File or directory not found.');
        }

        $perms = fileperms($fullPath);
        $permissions = $perms !== false ? decoct($perms & 0777) : '0000';

        return [
            'info' => [
                'permissions' => $permissions,
            ],
        ];
    }

    /**
     * Get the relative path from root.
     */
    protected function relativePath(string $absolutePath): string
    {
        $realRoot = realpath($this->rootPath);

        if ($realRoot === false) {
            return $absolutePath;
        }

        if ($absolutePath === $realRoot) {
            return '';
        }

        if (str_starts_with($absolutePath, $realRoot.'/')) {
            return substr($absolutePath, strlen($realRoot) + 1);
        }

        return $absolutePath;
    }

    /**
     * Recursively delete a directory.
     */
    protected function deleteDirectory(string $dir): void
    {
        if (! is_dir($dir)) {
            return;
        }

        $entries = scandir($dir);
        if ($entries === false) {
            throw new RuntimeException('Failed to read directory for deletion.');
        }

        foreach ($entries as $entry) {
            if ($entry === '.' || $entry === '..') {
                continue;
            }

            $entryPath = $dir.'/'.$entry;

            if (is_dir($entryPath)) {
                $this->deleteDirectory($entryPath);
            } else {
                if (! unlink($entryPath)) {
                    throw new RuntimeException("Failed to delete file: {$entry}");
                }
            }
        }

        if (! rmdir($dir)) {
            throw new RuntimeException('Failed to remove directory.');
        }
    }

    /**
     * Recursively copy a directory.
     */
    protected function copyDirectory(string $source, string $destination): void
    {
        if (! is_dir($destination)) {
            mkdir($destination, 0755, true);
        }

        $entries = scandir($source);
        if ($entries === false) {
            throw new RuntimeException('Failed to read source directory.');
        }

        foreach ($entries as $entry) {
            if ($entry === '.' || $entry === '..') {
                continue;
            }

            $srcPath = $source.'/'.$entry;
            $dstPath = $destination.'/'.$entry;

            if (is_dir($srcPath)) {
                $this->copyDirectory($srcPath, $dstPath);
            } else {
                if (! copy($srcPath, $dstPath)) {
                    throw new RuntimeException("Failed to copy: {$entry}");
                }
            }
        }
    }
}
