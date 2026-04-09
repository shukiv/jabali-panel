<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\HostingPackages\Schemas;

use Filament\Forms\Components\Select;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;

class HostingPackageForm
{
    public static function configure(Schema $schema): Schema
    {
        return $schema
            ->columns(1)
            ->components([
                Section::make(__('Package Details'))
                    ->schema([
                        TextInput::make('name')
                            ->label(__('Name'))
                            ->required()
                            ->maxLength(120)
                            ->unique(ignoreRecord: true),
                        Textarea::make('description')
                            ->label(__('Description'))
                            ->rows(3)
                            ->columnSpanFull(),
                        Toggle::make('is_active')
                            ->label(__('Active'))
                            ->default(true),
                        Select::make('ssh_isolation_mode')
                            ->label(__('SSH Access Mode'))
                            ->options([
                                'disabled' => __('Disabled — SFTP only'),
                                'container' => __('Container (nspawn) — high isolation'),
                                'sandbox' => __('Sandbox (bwrap) — moderate isolation, IDE-compatible'),
                                'standard' => __('Standard — plain shell, no isolation'),
                            ])
                            ->default('disabled')
                            ->helperText(__('Controls SSH shell access for users on this package. Disabled = SFTP only.')),
                    ])
                    ->columns(2),

                Section::make(__('Resource Limits'))
                    ->description(__('Leave blank for unlimited.'))
                    ->schema([
                        TextInput::make('disk_quota_mb')
                            ->label(__('Disk Quota (MB)'))
                            ->numeric()
                            ->minValue(0)
                            ->helperText(__('Example: 10240 = 10 GB')),
                        TextInput::make('bandwidth_gb')
                            ->label(__('Bandwidth (GB / month)'))
                            ->numeric()
                            ->minValue(0),
                        TextInput::make('domains_limit')
                            ->label(__('Domains Limit'))
                            ->numeric()
                            ->minValue(0),
                        TextInput::make('databases_limit')
                            ->label(__('Databases Limit'))
                            ->numeric()
                            ->minValue(0),
                        TextInput::make('mailboxes_limit')
                            ->label(__('Mailboxes Limit'))
                            ->numeric()
                            ->minValue(0),
                    ])
                    ->columns(2),

                Section::make(__('System Resource Limits'))
                    ->description(__('cgroup v2 limits applied per user. Leave blank for unlimited.'))
                    ->schema([
                        TextInput::make('cpu_quota')
                            ->label(__('CPU Quota (%)'))
                            ->numeric()
                            ->minValue(1)
                            ->maxValue(800)
                            ->helperText(__('100 = 1 core. Typical: 100–200')),
                        TextInput::make('memory_limit_mb')
                            ->label(__('Memory Limit (MB)'))
                            ->numeric()
                            ->minValue(64)
                            ->helperText(__('Typical: 256–2048. WordPress needs ~256 minimum')),
                        TextInput::make('io_read_mbps')
                            ->label(__('I/O Read (MB/s)'))
                            ->numeric()
                            ->minValue(1)
                            ->helperText(__('Typical: 50–100')),
                        TextInput::make('io_write_mbps')
                            ->label(__('I/O Write (MB/s)'))
                            ->numeric()
                            ->minValue(1)
                            ->helperText(__('Typical: 25–50')),
                        TextInput::make('max_processes')
                            ->label(__('Max Processes'))
                            ->numeric()
                            ->minValue(10)
                            ->helperText(__('Typical: 50–200. Includes FPM workers + SSH sessions')),
                        TextInput::make('nginx_req_per_sec')
                            ->label(__('Requests / second'))
                            ->numeric()
                            ->minValue(1)
                            ->helperText(__('Typical: 30–100. Per IP, with short burst allowed')),
                        TextInput::make('nginx_connections')
                            ->label(__('Max Connections'))
                            ->numeric()
                            ->minValue(1)
                            ->helperText(__('Typical: 50–200. Concurrent connections per IP')),
                    ])
                    ->columns(2)
                    ->collapsible(),

            ]);
    }
}
