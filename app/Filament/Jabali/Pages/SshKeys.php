<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\Setting;
use App\Support\SafeError;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Support\Enums\FontWeight;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Facades\Auth;

class SshKeys extends Page implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-key';

    protected static ?int $navigationSort = 7;

    protected string $view = 'filament.jabali.pages.ssh-keys';

    public static function getNavigationLabel(): string
    {
        return __('SSH & SFTP');
    }

    public function getTitle(): string
    {
        return __('SSH & SFTP Access');
    }

    public array $sshKeys = [];

    public string $sshHost = '';

    public string $sshPort = '22';

    public string $sshUsername = '';

    public string $sshCommand = '';

    public ?array $generatedKey = null;

    public bool $shellEnabled = false;

    public bool $shellAccessAllowed = true;

    public function mount(): void
    {
        $this->shellAccessAllowed = Setting::get('ssh_shell_access_enabled', '1') === '1';
        $this->loadSshKeys();
        if ($this->shellAccessAllowed) {
            $this->loadShellStatus();
        } else {
            $this->shellEnabled = false;
        }
        $this->sshHost = request()->getHost();
        $this->sshPort = '22';
        $this->sshUsername = $this->getUsername();
        $this->sshCommand = 'ssh '.$this->sshUsername.'@'.$this->sshHost;
    }

    protected function loadShellStatus(): void
    {
        try {
            $result = $this->getAgent()->send('ssh.shell_status', ['username' => $this->getUsername()]);
            $this->shellEnabled = $result['shell_enabled'] ?? false;
        } catch (\Exception $e) {
            $this->shellEnabled = false;
        }
    }

    public function toggleShellAccess(): void
    {
        if (! $this->shellAccessAllowed) {
            Notification::make()
                ->title(__('Terminal Access Disabled'))
                ->body(__('Terminal access has been disabled by the administrator.'))
                ->warning()
                ->send();

            return;
        }

        try {
            $command = $this->shellEnabled ? 'ssh.disable_shell' : 'ssh.enable_shell';
            $result = $this->getAgent()->send($command, ['username' => $this->getUsername()]);

            if ($result['success'] ?? false) {
                $this->shellEnabled = ! $this->shellEnabled;
                Notification::make()
                    ->title($this->shellEnabled ? __('SSH Shell Enabled') : __('SSH Shell Disabled'))
                    ->body($this->shellEnabled
                        ? __('You now have jailed shell access with wp-cli support (you can run wp-cli here).')
                        : __('Shell access disabled. You can still use SFTP for file transfers.'))
                    ->success()
                    ->send();
            } else {
                throw new \Exception($result['error'] ?? __('Failed to toggle shell access'));
            }
        } catch (\Exception $e) {
            Notification::make()
                ->title(__('Error'))
                ->body(SafeError::message($e))
                ->danger()
                ->send();
        }
    }

    protected function getUsername(): string
    {
        $user = Auth::user();

        return $user->system_username ?? $user->username ?? $user->email;
    }

    protected function getAgent()
    {
        return app(\App\Services\Agent\AgentClient::class);
    }

    protected function loadSshKeys(): void
    {
        try {
            $result = $this->getAgent()->send('ssh.list_keys', ['username' => $this->getUsername()]);
            $this->sshKeys = $result['keys'] ?? [];
        } catch (\Exception $e) {
            $this->sshKeys = [];
        }
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->sshKeys)
            ->columns([
                TextColumn::make('name')
                    ->label(__('Key Name'))
                    ->icon('heroicon-o-key')
                    ->iconColor('success')
                    ->weight(FontWeight::Medium)
                    ->searchable(),
                TextColumn::make('fingerprint')
                    ->label(__('Fingerprint'))
                    ->fontFamily('mono')
                    ->size('sm')
                    ->color('gray')
                    ->limit(40),
                TextColumn::make('added_at')
                    ->label(__('Added'))
                    ->date('M d, Y')
                    ->sortable(),
            ])
            ->recordActions([
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Delete SSH Key'))
                    ->modalDescription(__('Are you sure you want to delete this SSH key? You will no longer be able to use it to connect to the server.'))
                    ->modalIcon('heroicon-o-trash')
                    ->modalIconColor('danger')
                    ->modalSubmitActionLabel(__('Delete Key'))
                    ->action(function (array $record): void {
                        try {
                            $result = $this->getAgent()->send('ssh.delete_key', [
                                'username' => $this->getUsername(),
                                'key_id' => $record['id'],
                            ]);

                            if ($result['success'] ?? false) {
                                Notification::make()
                                    ->title(__('SSH Key Deleted'))
                                    ->success()
                                    ->send();
                                $this->loadSshKeys();
                                $this->resetTable();
                            } else {
                                throw new \Exception($result['error'] ?? __('Failed to delete key'));
                            }
                        } catch (\Exception $e) {
                            Notification::make()
                                ->title(__('Error'))
                                ->body(SafeError::message($e))
                                ->danger()
                                ->send();
                        }
                    }),
            ])
            ->emptyStateHeading(__('No SSH keys added yet'))
            ->emptyStateDescription(__('Click "Generate SSH Key" to create a new key pair, or "Add Existing Key" to add your own public key'))
            ->emptyStateIcon('heroicon-o-key')
            ->striped();
    }

    public function getTableRecordKey(Model|array $record): string
    {
        return is_array($record) ? ($record['id'] ?? $record['name']) : $record->getKey();
    }

    protected function getHeaderActions(): array
    {
        return [
            Action::make('generateKey')
                ->label(__('Generate SSH Key'))
                ->icon('heroicon-o-sparkles')
                ->color('success')
                ->modalHeading(__('Generate SSH Key'))
                ->modalDescription(__('Generate a new SSH key pair for secure server access'))
                ->modalIcon('heroicon-o-sparkles')
                ->modalIconColor('success')
                ->modalSubmitActionLabel(__('Generate Key'))
                ->form([
                    TextInput::make('name')
                        ->label(__('Key Name'))
                        ->placeholder(__('My Generated Key'))
                        ->required()
                        ->maxLength(50)
                        ->helperText(__('A descriptive name to identify this key')),
                    Select::make('type')
                        ->label(__('Key Type'))
                        ->options([
                            'ed25519' => __('ED25519 (Recommended)'),
                            'rsa' => __('RSA 4096-bit'),
                        ])
                        ->default('ed25519')
                        ->required()
                        ->helperText(__('ED25519 is faster and more secure')),
                    TextInput::make('passphrase')
                        ->label(__('Passphrase (Optional)'))
                        ->password()
                        ->helperText(__('Leave empty for no passphrase')),
                ])
                ->action(function (array $data): void {
                    try {
                        $result = $this->generateSshKey($data['name'], $data['type'], $data['passphrase'] ?? '');

                        if ($result) {
                            $this->generatedKey = $result;
                            $this->loadSshKeys();

                            Notification::make()
                                ->title(__('SSH Key Generated!'))
                                ->body(__('Download your private key below. The public key has been added to your account.'))
                                ->success()
                                ->persistent()
                                ->send();
                        }
                    } catch (\Exception $e) {
                        Notification::make()
                            ->title(__('Error'))
                            ->body(SafeError::message($e))
                            ->danger()
                            ->send();
                    }
                }),
            Action::make('addKey')
                ->label(__('Add Existing Key'))
                ->icon('heroicon-o-plus')
                ->color('primary')
                ->modalHeading(__('Add Existing SSH Key'))
                ->modalDescription(__('Add your existing public key to enable SSH access'))
                ->modalIcon('heroicon-o-key')
                ->modalIconColor('primary')
                ->modalSubmitActionLabel(__('Add Key'))
                ->form([
                    TextInput::make('name')
                        ->label(__('Key Name'))
                        ->placeholder(__('My Laptop'))
                        ->required()
                        ->maxLength(50)
                        ->helperText(__('A descriptive name to identify this key')),
                    Textarea::make('public_key')
                        ->label(__('Public Key'))
                        ->placeholder(__('ssh-rsa AAAAB3... or ssh-ed25519 AAAAC3...'))
                        ->required()
                        ->rows(4)
                        ->helperText(__('Paste your public key (usually from ~/.ssh/id_rsa.pub or ~/.ssh/id_ed25519.pub)')),
                ])
                ->action(function (array $data): void {
                    try {
                        $result = $this->getAgent()->send('ssh.add_key', [
                            'username' => $this->getUsername(),
                            'name' => $data['name'],
                            'public_key' => trim($data['public_key']),
                        ]);

                        if ($result['success'] ?? false) {
                            Notification::make()
                                ->title(__('SSH Key Added'))
                                ->success()
                                ->send();
                            $this->loadSshKeys();
                        } else {
                            throw new \Exception($result['error'] ?? __('Failed to add key'));
                        }
                    } catch (\Exception $e) {
                        Notification::make()
                            ->title(__('Error'))
                            ->body(SafeError::message($e))
                            ->danger()
                            ->send();
                    }
                }),
        ];
    }

    protected function generateSshKey(string $name, string $type, string $passphrase = ''): array
    {
        $result = $this->getAgent()->send('ssh.generate_key', [
            'name' => $name,
            'type' => $type,
            'passphrase' => $passphrase,
        ]);

        if (! ($result['success'] ?? false)) {
            throw new \Exception($result['error'] ?? __('Failed to generate SSH key'));
        }

        // Add public key to user authorized_keys
        $addResult = $this->getAgent()->send('ssh.add_key', [
            'username' => $this->getUsername(),
            'name' => $name,
            'public_key' => trim($result['public_key']),
        ]);

        if (! ($addResult['success'] ?? false)) {
            throw new \Exception($addResult['error'] ?? __('Failed to add key to server'));
        }

        return [
            'name' => $result['name'],
            'type' => $result['type'],
            'private_key' => $result['private_key'],
            'public_key' => $result['public_key'],
            'ppk_key' => $result['ppk_key'] ?? null,
            'fingerprint' => $result['fingerprint'] ?? '',
        ];
    }

    public function clearGeneratedKey(): void
    {
        $this->generatedKey = null;
    }

    public function downloadPrivateKey(): \Symfony\Component\HttpFoundation\StreamedResponse
    {
        if (! $this->generatedKey) {
            abort(404);
        }

        $key = $this->generatedKey['private_key'];
        $name = preg_replace('/[^a-zA-Z0-9_-]/', '_', $this->generatedKey['name']);

        return response()->streamDownload(function () use ($key) {
            echo $key;
        }, "id_{$this->generatedKey['type']}_{$name}", [
            'Content-Type' => 'application/x-pem-file',
        ]);
    }

    public function downloadPpkKey(): \Symfony\Component\HttpFoundation\StreamedResponse
    {
        if (! $this->generatedKey || ! $this->generatedKey['ppk_key']) {
            abort(404);
        }

        $key = $this->generatedKey['ppk_key'];
        $name = preg_replace('/[^a-zA-Z0-9_-]/', '_', $this->generatedKey['name']);

        return response()->streamDownload(function () use ($key) {
            echo $key;
        }, "{$name}.ppk", [
            'Content-Type' => 'application/x-putty-private-key',
        ]);
    }

    public function downloadFileZillaConfig(): \Symfony\Component\HttpFoundation\StreamedResponse
    {
        $xml = $this->generateFileZillaXml();

        return response()->streamDownload(function () use ($xml) {
            echo $xml;
        }, 'jabali-sftp.xml', [
            'Content-Type' => 'application/xml',
        ]);
    }

    public function downloadWinScpConfig(): \Symfony\Component\HttpFoundation\StreamedResponse
    {
        $ini = $this->generateWinScpIni();

        return response()->streamDownload(function () use ($ini) {
            echo $ini;
        }, 'jabali-sftp.ini', [
            'Content-Type' => 'text/plain',
        ]);
    }

    protected function generateFileZillaXml(): string
    {
        $host = htmlspecialchars($this->sshHost);
        $port = htmlspecialchars($this->sshPort);
        $user = htmlspecialchars($this->sshUsername);

        return <<<XML
<?xml version="1.0" encoding="UTF-8"?>
<FileZilla3 version="3.66.4" platform="*">
    <Servers>
        <Server>
            <Host>{$host}</Host>
            <Port>{$port}</Port>
            <Protocol>1</Protocol>
            <Type>0</Type>
            <User>{$user}</User>
            <Logontype>1</Logontype>
            <EncodingType>Auto</EncodingType>
            <BypassProxy>0</BypassProxy>
            <Name>Jabali - {$host}</Name>
            <RemoteDir>/home/{$user}</RemoteDir>
            <SyncBrowsing>0</SyncBrowsing>
            <DirectoryComparison>0</DirectoryComparison>
        </Server>
    </Servers>
</FileZilla3>
XML;
    }

    protected function generateWinScpIni(): string
    {
        $host = $this->sshHost;
        $port = $this->sshPort;
        $user = $this->sshUsername;
        $sessionName = "Jabali - {$host}";

        return <<<INI
[Sessions\\{$sessionName}]
HostName={$host}
PortNumber={$port}
UserName={$user}
FSProtocol=2
RemoteDirectory=/home/{$user}
UpdateDirectories=1
INI;
    }
}
