<?php

declare(strict_types=1);

namespace App\FileBrowser\Adapters;

use App\FileBrowser\Support\PathSanitizer;
use App\FileBrowser\ValueObjects\FilePermission;
use Illuminate\Support\Facades\Process;
use RuntimeException;

class SshRsyncAdapter implements Archiver, FileBrowserAdapter, FileOperations, PermissionManager
{
    protected string $host;

    protected int $port;

    protected string $username;

    protected ?string $privateKey;

    protected string $remotePath;

    protected string $rsyncOptions;

    protected int $timeout;

    private PathSanitizer $paths;

    public static function fromConfig(array $config): static
    {
        return new static($config);
    }

    public function __construct(array $config)
    {
        $this->host = $config['host'] ?? '';
        $this->port = (int) ($config['port'] ?? 22);
        $this->username = $config['username'] ?? '';
        $this->privateKey = $config['private_key'] ?? null;
        $this->remotePath = rtrim($config['remote_path'] ?? '/', '/');
        $this->rsyncOptions = $this->validateRsyncOptions($config['rsync_options'] ?? '-avz --progress');
        $this->timeout = (int) ($config['timeout'] ?? 60);

        if ($this->host === '' || $this->username === '') {
            throw new RuntimeException('SSH host and username are required.');
        }

        $this->paths = PathSanitizer::remote($this->remotePath);
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

    /**
     * Validate rsync options to prevent command injection.
     * Only allows known safe rsync flags.
     */
    protected function validateRsyncOptions(string $options): string
    {
        $allowedFlags = [
            '-a', '-v', '-z', '-r', '-l', '-p', '-t', '-g', '-o', '-D',
            '-H', '-S', '-n', '-q', '-c',
            '--archive', '--verbose', '--compress', '--recursive',
            '--links', '--perms', '--times', '--group', '--owner',
            '--devices', '--specials', '--hard-links', '--sparse',
            '--dry-run', '--quiet', '--checksum', '--progress',
            '--human-readable', '--partial', '--delete',
        ];

        $tokens = preg_split('/\s+/', trim($options), -1, PREG_SPLIT_NO_EMPTY);
        $validated = [];

        foreach ($tokens as $token) {
            // Allow combined short flags like -avz
            if (preg_match('/^-[avzrlptgoDHSnqc]+$/', $token)) {
                $validated[] = $token;

                continue;
            }

            if (in_array($token, $allowedFlags, true)) {
                $validated[] = $token;
            }
            // Skip unrecognized flags silently for safety
        }

        return implode(' ', $validated) ?: '-avz';
    }

    public function list(string $path, bool $showHidden = false): array
    {
        $fullPath = $this->paths->resolve($path);
        $hiddenFlag = $showHidden ? '-la' : '-l';

        $command = $this->buildSshCommand(
            'ls '.$hiddenFlag.' --time-style=+%s '.escapeshellarg($fullPath)
        );

        $result = Process::timeout($this->timeout)->run($command);

        if (! $result->successful()) {
            throw new RuntimeException('Failed to list directory: '.$result->errorOutput());
        }

        $lines = explode("\n", trim($result->output()));
        $items = [];

        foreach ($lines as $line) {
            $line = trim($line);

            // Skip total line and empty lines
            if ($line === '' || str_starts_with($line, 'total ')) {
                continue;
            }

            $parsed = $this->parseLsLine($line, $path);
            if ($parsed === null) {
                continue;
            }

            if ($parsed['name'] === '.' || $parsed['name'] === '..') {
                continue;
            }

            if (! $showHidden && str_starts_with($parsed['name'], '.')) {
                continue;
            }

            $items[] = $parsed;
        }

        return ['items' => $items];
    }

    public function read(string $path): array
    {
        $fullPath = $this->paths->resolve($path);

        $command = $this->buildSshCommand(
            'base64 '.escapeshellarg($fullPath)
        );

        $result = Process::timeout($this->timeout)->run($command);

        if (! $result->successful()) {
            throw new RuntimeException('Failed to read file: '.$result->errorOutput());
        }

        return ['content' => trim($result->output())];
    }

    public function write(string $path, string $content): array
    {
        $fullPath = $this->paths->resolve($path);

        $command = $this->buildSshCommand(
            'cat > '.escapeshellarg($fullPath)
        );

        $result = Process::timeout($this->timeout)->input($content)->run($command);

        if (! $result->successful()) {
            throw new RuntimeException('Failed to write file: '.$result->errorOutput());
        }

        return ['success' => true];
    }

    public function delete(string $path): array
    {
        $fullPath = $this->paths->resolve($path);

        $command = $this->buildSshCommand(
            'rm -rf '.escapeshellarg($fullPath)
        );

        $result = Process::timeout($this->timeout)->run($command);

        if (! $result->successful()) {
            throw new RuntimeException('Failed to delete: '.$result->errorOutput());
        }

        return ['success' => true];
    }

    public function mkdir(string $path): array
    {
        $fullPath = $this->paths->resolve($path);

        $command = $this->buildSshCommand(
            'mkdir -p '.escapeshellarg($fullPath)
        );

        $result = Process::timeout($this->timeout)->run($command);

        if (! $result->successful()) {
            throw new RuntimeException('Failed to create directory: '.$result->errorOutput());
        }

        return ['success' => true];
    }

    public function rename(string $oldPath, string $newPath): array
    {
        $fullOld = $this->paths->resolve($oldPath);
        $fullNew = $this->paths->resolve($newPath);

        $command = $this->buildSshCommand(
            'mv '.escapeshellarg($fullOld).' '.escapeshellarg($fullNew)
        );

        $result = Process::timeout($this->timeout)->run($command);

        if (! $result->successful()) {
            throw new RuntimeException('Failed to rename: '.$result->errorOutput());
        }

        return ['success' => true];
    }

    public function copy(string $source, string $destination): array
    {
        $fullSource = $this->paths->resolve($source);
        $fullDest = $this->paths->resolve($destination);

        $command = $this->buildSshCommand(
            'cp -r '.escapeshellarg($fullSource).' '.escapeshellarg($fullDest)
        );

        $result = Process::timeout($this->timeout)->run($command);

        if (! $result->successful()) {
            throw new RuntimeException('Failed to copy: '.$result->errorOutput());
        }

        return ['success' => true];
    }

    public function move(string $source, string $destination): array
    {
        $fullSource = $this->paths->resolve($source);
        $fullDest = $this->paths->resolve($destination);

        $command = $this->buildSshCommand(
            'mv '.escapeshellarg($fullSource).' '.escapeshellarg($fullDest)
        );

        $result = Process::timeout($this->timeout)->run($command);

        if (! $result->successful()) {
            throw new RuntimeException('Failed to move: '.$result->errorOutput());
        }

        return ['success' => true];
    }

    public function upload(string $directory, string $filename, string $content): array
    {
        $sanitized = PathSanitizer::filename($filename);

        // Write content to a temp file
        $tempFile = tempnam(sys_get_temp_dir(), 'jabali_upload_');
        if ($tempFile === false) {
            throw new RuntimeException('Failed to create temporary file.');
        }

        try {
            file_put_contents($tempFile, $content);

            $remoteDir = $this->paths->resolve($directory);
            $remoteDest = $remoteDir.'/'.$sanitized;

            $sshArgs = $this->buildSshArgs();
            $remoteSpec = escapeshellarg($this->username.'@'.$this->host.':'.$remoteDest);

            $command = 'rsync '.$this->rsyncOptions
                .' -e '.escapeshellarg('ssh '.$sshArgs)
                .' '.escapeshellarg($tempFile)
                .' '.$remoteSpec;

            $result = Process::timeout($this->timeout)->run($command);

            if (! $result->successful()) {
                throw new RuntimeException('Failed to upload: '.$result->errorOutput());
            }
        } finally {
            @unlink($tempFile);
        }

        $relativePath = ltrim($directory, '/').'/'.$sanitized;

        return ['success' => true, 'path' => $relativePath];
    }

    public function extract(string $path): array
    {
        $fullPath = $this->paths->resolve($path);
        $dir = dirname($fullPath);
        $extension = strtolower(pathinfo($fullPath, PATHINFO_EXTENSION));

        if ($extension === 'zip') {
            $extractCommand = 'cd '.escapeshellarg($dir).' && unzip -o '.escapeshellarg($fullPath);
        } elseif (in_array($extension, ['gz', 'bz2', 'tar', 'tgz'])) {
            $extractCommand = 'cd '.escapeshellarg($dir).' && tar xf '.escapeshellarg($fullPath);
        } else {
            throw new RuntimeException('Unsupported archive format.');
        }

        $command = $this->buildSshCommand($extractCommand);
        $result = Process::timeout($this->timeout)->run($command);

        if (! $result->successful()) {
            throw new RuntimeException('Failed to extract: '.$result->errorOutput());
        }

        return ['success' => true];
    }

    public function chmod(string $path, string $mode): array
    {
        if (! preg_match('/^[0-7]{3,4}$/', $mode)) {
            throw new RuntimeException('Invalid permission mode.');
        }

        $fullPath = $this->paths->resolve($path);

        $command = $this->buildSshCommand(
            'chmod '.escapeshellarg($mode).' '.escapeshellarg($fullPath)
        );

        $result = Process::timeout($this->timeout)->run($command);

        if (! $result->successful()) {
            throw new RuntimeException('Failed to change permissions: '.$result->errorOutput());
        }

        return ['success' => true];
    }

    public function info(string $path): array
    {
        $fullPath = $this->paths->resolve($path);

        $command = $this->buildSshCommand(
            'stat -c '.escapeshellarg('%a').' '.escapeshellarg($fullPath)
        );

        $result = Process::timeout($this->timeout)->run($command);

        if (! $result->successful()) {
            throw new RuntimeException('Failed to get file info: '.$result->errorOutput());
        }

        return [
            'info' => [
                'permissions' => trim($result->output()),
            ],
        ];
    }

    /**
     * Build the SSH command string with proper escaping.
     */
    protected function buildSshCommand(string $remoteCommand): string
    {
        $args = $this->buildSshArgs();

        return 'ssh '.$args.' '.escapeshellarg($this->username.'@'.$this->host)
            .' '.escapeshellarg($remoteCommand);
    }

    /**
     * Build common SSH arguments.
     */
    protected function buildSshArgs(): string
    {
        $args = '-o StrictHostKeyChecking=accept-new -p '.escapeshellarg((string) $this->port);

        if ($this->privateKey !== null && $this->privateKey !== '') {
            $args .= ' -i '.escapeshellarg($this->privateKey);
        }

        return $args;
    }

    /**
     * Parse a single line from `ls -l --time-style=+%s` output.
     */
    protected function parseLsLine(string $line, string $basePath): ?array
    {
        // Format: permissions links owner group size timestamp name
        // Example: drwxr-xr-x 2 user group 4096 1700000000 dirname
        $parts = preg_split('/\s+/', $line, 7);

        if ($parts === false || count($parts) < 7) {
            return null;
        }

        $permString = $parts[0];
        $size = (int) $parts[4];
        $modified = (int) $parts[5];
        $name = $parts[6];

        $isDir = str_starts_with($permString, 'd');

        // Convert permission string to octal
        $permissions = FilePermission::fromUnixString($permString ?: 'drwxr-xr-x')->toOctal();

        $relativePath = ltrim($basePath, '/');
        $itemPath = $relativePath !== '' ? $relativePath.'/'.$name : $name;

        return [
            'name' => $name,
            'path' => $itemPath,
            'is_dir' => $isDir,
            'size' => $isDir ? null : $size,
            'modified' => $modified,
            'permissions' => $permissions,
        ];
    }
}
