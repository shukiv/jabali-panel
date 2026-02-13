<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

use App\Models\Domain;
use App\Models\Mailbox;
use App\Services\Agent\AgentClient;
use Filament\Widgets\Widget;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\DB;

class StatsOverview extends Widget
{
    protected static ?int $sort = 1;

    protected int|string|array $columnSpan = 'full';

    protected string $view = 'filament.jabali.widgets.stats-overview';

    public function getStats(): array
    {
        $user = Auth::user();

        // Count domains
        $domainCount = Domain::where('user_id', $user->id)->count();

        // Count email accounts
        $emailCount = Mailbox::whereHas('emailDomain', function ($query) use ($user) {
            $query->whereHas('domain', function ($q) use ($user) {
                $q->where('user_id', $user->id);
            });
        })->count();

        // Count databases
        $dbCount = 0;
        $dbCountResolved = false;

        try {
            $agent = app(AgentClient::class);
            $result = $agent->mysqlListDatabases($user->username);

            if (($result['success'] ?? false) === true) {
                $dbCountResolved = true;
                $dbCount = count($result['databases'] ?? []);
            }
        } catch (\Exception $e) {
            // Ignore agent errors and fall back to direct DB query
        }

        if (! $dbCountResolved) {
            $prefix = $user->username.'_';
            $connections = [config('database.default')];
            if ($connections[0] === 'sqlite') {
                $connections = ['mysql', 'mariadb', 'sqlite'];
            }

            foreach ($connections as $connection) {
                try {
                    $databases = DB::connection($connection)->select('SHOW DATABASES LIKE ?', [$prefix.'%']);
                    $dbCount = count($databases);
                    if ($dbCount > 0) {
                        break;
                    }
                } catch (\Exception $e) {
                    // Try next connection
                }
            }
        }

        // Count SSL certificates
        $sslCount = Domain::where('user_id', $user->id)
            ->whereHas('sslCertificate', function ($query) {
                $query->where('expires_at', '>', now());
            })->count();

        return [
            [
                'value' => $domainCount,
                'label' => __('Domains'),
                'icon' => 'heroicon-o-globe-alt',
                'color' => 'success',
            ],
            [
                'value' => $emailCount,
                'label' => __('Mailboxes'),
                'icon' => 'heroicon-o-envelope',
                'color' => 'info',
            ],
            [
                'value' => $dbCount,
                'label' => __('Databases'),
                'icon' => 'heroicon-o-circle-stack',
                'color' => 'warning',
            ],
            [
                'value' => $sslCount,
                'label' => __('SSL Certificates'),
                'icon' => 'heroicon-o-lock-closed',
                'color' => 'success',
            ],
        ];
    }
}
