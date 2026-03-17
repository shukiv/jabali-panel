<?php

declare(strict_types=1);

namespace App\Livewire;

use App\Models\MysqlCredential;
use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\CheckboxList;
use Filament\Forms\Components\Radio;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Columns\ViewColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\Crypt;
use Livewire\Component;

class DatabaseUsersTable extends Component implements HasActions, HasForms, HasTable
{
    use InteractsWithActions;
    use InteractsWithForms;
    use InteractsWithTable;

    public array $users = [];

    public array $userGrants = [];

    public array $databases = [];

    public ?string $selectedUser = null;

    protected $listeners = ['refresh-database-users' => 'refreshData'];

    public function mount(): void
    {
        $this->loadData();
    }

    public function refreshData(): void
    {
        $this->loadData();
        $this->resetTable();
    }

    public function getAgent(): AgentClient
    {
        return app(AgentClient::class);
    }

    public function getUsername(): string
    {
        return Auth::user()->username;
    }

    public function loadData(): void
    {
        try {
            $result = $this->getAgent()->mysqlListUsers($this->getUsername());
            $this->users = $result['users'] ?? [];

            // Filter out the master admin user
            $this->users = array_values(array_filter($this->users, function ($user) {
                return $user['user'] !== $this->getUsername().'_admin';
            }));

            $this->userGrants = [];
            foreach ($this->users as $user) {
                $this->loadUserGrants($user['user'], $user['host']);
            }
        } catch (Exception $e) {
            $this->users = [];
        }

        try {
            $result = $this->getAgent()->mysqlListDatabases($this->getUsername());
            $this->databases = $result['databases'] ?? [];
        } catch (Exception $e) {
            $this->databases = [];
        }
    }

    protected function loadUserGrants(string $user, string $host): void
    {
        try {
            $result = $this->getAgent()->mysqlGetPrivileges($this->getUsername(), $user, $host);
            $this->userGrants["$user@$host"] = $result['parsed'] ?? [];
        } catch (Exception $e) {
            $this->userGrants["$user@$host"] = [];
        }
    }

    public function getUserGrants(string $user, string $host): array
    {
        return $this->userGrants["$user@$host"] ?? [];
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->users)
            ->columns([
                TextColumn::make('user')
                    ->label(__('User'))
                    ->icon('heroicon-o-user')
                    ->iconColor('primary')
                    ->description(fn (array $record): string => '@ '.$record['host'])
                    ->weight('medium')
                    ->searchable(),
                ViewColumn::make('privileges')
                    ->label(__('Database Privileges'))
                    ->view('filament.jabali.tables.columns.user-privileges'),
            ])
            ->recordActions([
                Action::make('addPrivileges')
                    ->label(__('Add Access'))
                    ->icon('heroicon-o-plus')
                    ->color('success')
                    ->modalHeading(__('Add Database Access'))
                    ->modalDescription(fn (array $record): string => __('Grant privileges to :user', ['user' => $record['user'].'@'.$record['host']]))
                    ->modalIcon('heroicon-o-shield-check')
                    ->modalIconColor('success')
                    ->modalWidth('lg')
                    ->modalSubmitActionLabel(__('Grant Access'))
                    ->form(fn (array $record): array => $this->getPrivilegesForm())
                    ->action(function (array $data, array $record): void {
                        $this->grantPrivileges($record['user'], $record['host'], $data);
                    }),
                Action::make('changePassword')
                    ->label(__('Password'))
                    ->icon('heroicon-o-key')
                    ->color('warning')
                    ->modalHeading(__('Change Password'))
                    ->modalDescription(fn (array $record): string => $record['user'].'@'.$record['host'])
                    ->modalIcon('heroicon-o-key')
                    ->modalIconColor('warning')
                    ->modalSubmitActionLabel(__('Change Password'))
                    ->form([
                        TextInput::make('password')
                            ->label(__('New Password'))
                            ->password()
                            ->revealable()
                            ->required()
                            ->minLength(8)
                            ->default(fn () => $this->generateSecurePassword())
                            ->suffixActions([
                                Action::make('generate')
                                    ->icon('heroicon-o-arrow-path')
                                    ->tooltip(__('Generate secure password'))
                                    ->action(fn ($set) => $set('password', $this->generateSecurePassword())),
                                Action::make('copy')
                                    ->icon('heroicon-o-clipboard-document')
                                    ->tooltip(__('Copy to clipboard'))
                                    ->action(function ($state, $livewire) {
                                        if ($state) {
                                            $livewire->js('navigator.clipboard.writeText('.json_encode($state, JSON_HEX_TAG).')');
                                            Notification::make()
                                                ->title(__('Copied to clipboard'))
                                                ->success()
                                                ->duration(2000)
                                                ->send();
                                        }
                                    }),
                            ]),
                    ])
                    ->action(function (array $data, array $record): void {
                        $this->changePassword($record['user'], $record['host'], $data['password']);
                    }),
                Action::make('delete')
                    ->label(__('Delete'))
                    ->icon('heroicon-o-trash')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('Delete User'))
                    ->modalDescription(fn (array $record): string => __("Delete user ':user'?", ['user' => $record['user'].'@'.$record['host']]))
                    ->modalIcon('heroicon-o-trash')
                    ->modalIconColor('danger')
                    ->action(function (array $record): void {
                        $this->deleteUser($record['user'], $record['host']);
                    }),
            ])
            ->emptyStateHeading(__('No database users yet'))
            ->emptyStateDescription(__('Click "New User" to create one'))
            ->emptyStateIcon('heroicon-o-users')
            ->striped();
    }

    public function getTableRecordKey(Model|array $record): string
    {
        return is_array($record) ? $record['user'].'@'.$record['host'] : $record->getKey();
    }

    protected function getPrivilegesForm(): array
    {
        $dbOptions = [];
        foreach ($this->databases as $db) {
            $dbOptions[$db['name']] = $db['name'];
        }

        return [
            Select::make('database')
                ->label(__('Database'))
                ->options($dbOptions)
                ->required()
                ->searchable()
                ->live(),
            Radio::make('privilege_type')
                ->label(__('Privilege Type'))
                ->options([
                    'all' => __('ALL PRIVILEGES'),
                    'specific' => __('Specific privileges'),
                ])
                ->default('all')
                ->required()
                ->live(),
            CheckboxList::make('specific_privileges')
                ->label(__('Select Privileges'))
                ->options([
                    'SELECT' => 'SELECT',
                    'INSERT' => 'INSERT',
                    'UPDATE' => 'UPDATE',
                    'DELETE' => 'DELETE',
                    'CREATE' => 'CREATE',
                    'DROP' => 'DROP',
                    'INDEX' => 'INDEX',
                    'ALTER' => 'ALTER',
                ])
                ->columns(2)
                ->visible(fn (callable $get): bool => $get('privilege_type') === 'specific'),
        ];
    }

    public function grantPrivileges(string $user, string $host, array $data): void
    {
        $privs = $data['privilege_type'] === 'all' ? ['ALL'] : ($data['specific_privileges'] ?? []);

        try {
            $this->getAgent()->mysqlGrantPrivileges($this->getUsername(), $user, $data['database'], $privs, $host);
            Notification::make()->title(__('Privileges granted'))->success()->send();
            $this->loadData();
            $this->resetTable();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function revokePrivileges(string $user, string $host, string $database): void
    {
        try {
            $this->getAgent()->mysqlRevokePrivileges($this->getUsername(), $user, $database, $host);
            Notification::make()->title(__('Access revoked'))->success()->send();
            $this->loadData();
            $this->resetTable();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function changePassword(string $user, string $host, string $password): void
    {
        try {
            $this->getAgent()->mysqlChangePassword($this->getUsername(), $user, $password, $host);

            MysqlCredential::updateOrCreate(
                ['user_id' => Auth::id(), 'mysql_username' => $user],
                ['mysql_password_encrypted' => Crypt::encryptString($password)]
            );

            Notification::make()->title(__('Password changed'))->success()->send();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function deleteUser(string $user, string $host): void
    {
        try {
            $this->getAgent()->mysqlDeleteUser($this->getUsername(), $user, $host);
            MysqlCredential::where('user_id', Auth::id())->where('mysql_username', $user)->delete();
            Notification::make()->title(__('User deleted'))->success()->send();
            $this->loadData();
            $this->resetTable();
        } catch (Exception $e) {
            Notification::make()->title(__('Error'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function generateSecurePassword(int $length = 16): string
    {
        $chars = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*';
        $password = '';
        for ($i = 0; $i < $length; $i++) {
            $password .= $chars[random_int(0, strlen($chars) - 1)];
        }

        return $password;
    }

    public function render()
    {
        return view('livewire.database-users-table');
    }
}
