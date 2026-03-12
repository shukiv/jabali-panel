<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use Exception;
use Illuminate\Console\Command;
use Symfony\Component\Process\Process;

class ConfigureDovecotAclCommand extends Command
{
    protected $signature = 'jabali:configure-dovecot-acl';

    protected $description = 'Configure Dovecot ACL plugin and shared namespace';

    public function handle(): int
    {
        $this->info('Configuring Dovecot ACL plugin...');

        // Check if already configured
        if (file_exists('/etc/dovecot/conf.d/90-acl.conf')) {
            $this->warn('ACL configuration already exists at /etc/dovecot/conf.d/90-acl.conf');

            return 0;
        }

        // Read DB credentials
        $dbPass = '';
        $dbHost = '127.0.0.1';
        $dbUser = 'jabali';
        $dbName = 'jabali';

        if (file_exists('/root/.jabali_db_credentials')) {
            $lines = file('/root/.jabali_db_credentials', FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES);
            foreach ($lines as $line) {
                if (str_starts_with($line, 'DB_PASSWORD=')) {
                    $dbPass = substr($line, strlen('DB_PASSWORD='));
                }
            }
        } elseif (file_exists(base_path('.env'))) {
            $dbPass = config('database.connections.mysql.password', '');
            $dbHost = config('database.connections.mysql.host', '127.0.0.1');
            $dbUser = config('database.connections.mysql.username', 'jabali');
            $dbName = config('database.connections.mysql.database', 'jabali');
        }

        // Update 10-mail.conf to add shared namespace if not present
        $this->info('  Updating mail namespace configuration...');
        $mailConf = '/etc/dovecot/conf.d/10-mail.conf';
        if (file_exists($mailConf)) {
            $content = file_get_contents($mailConf);
            if (! str_contains($content, 'namespace shared')) {
                $sharedNamespace = <<<'CONF'

namespace shared {
  type = shared
  separator = /
  prefix = shared/$user/
  mail_path = %{owner_home}
  mail_index_private_path = ~/shared/%{owner_user}
  subscriptions = no
  list = children
}

mail_plugins {
  acl = yes
}

protocol imap {
  mail_plugins {
    imap_acl = yes
  }
}

acl_driver = vfile

acl_sharing_map {
  dict proxy {
    name = acl
  }
}
CONF;
                file_put_contents($mailConf, $content.$sharedNamespace);
                $this->line('  Added shared namespace and ACL config to 10-mail.conf');
            } else {
                $this->line('  Shared namespace already present in 10-mail.conf');
            }
        } else {
            $this->error('  10-mail.conf not found');

            return 1;
        }

        // Write dict-sql config
        $this->info('  Writing dict-sql configuration...');
        $dictSqlConf = <<<CONF
connect = host={$dbHost} dbname={$dbName} user={$dbUser} password={$dbPass}
map {
  pattern = shared/shared-boxes/user/\$to/\$from
  table = user_shares
  value_field = dummy
  fields {
    from_user = \$from
    to_user = \$to
  }
}
CONF;
        file_put_contents('/etc/dovecot/dovecot-dict-sql.conf.ext', $dictSqlConf);
        chown('/etc/dovecot/dovecot-dict-sql.conf.ext', 'root');
        chgrp('/etc/dovecot/dovecot-dict-sql.conf.ext', 'dovecot');
        chmod('/etc/dovecot/dovecot-dict-sql.conf.ext', 0640);
        $this->line('  Created /etc/dovecot/dovecot-dict-sql.conf.ext');

        // Add dict definition to dovecot.conf
        $this->info('  Configuring dict definition...');
        $dovecotConf = '/etc/dovecot/dovecot.conf';
        if (file_exists($dovecotConf)) {
            $content = file_get_contents($dovecotConf);
            if (! str_contains($content, 'dict {')) {
                $dictDef = <<<'CONF'

# ACL sharing dict
dict {
  acl = mysql:/etc/dovecot/dovecot-dict-sql.conf.ext
}
CONF;
                file_put_contents($dovecotConf, $content.$dictDef);
                $this->line('  Added dict definition to dovecot.conf');
            } else {
                $this->line('  Dict definition already present in dovecot.conf');
            }
        }

        // Add dict service to 10-master.conf
        $this->info('  Configuring dict service...');
        $masterConf = '/etc/dovecot/conf.d/10-master.conf';
        if (file_exists($masterConf)) {
            $content = file_get_contents($masterConf);
            if (! str_contains($content, 'Dict service for ACL sharing')) {
                $dictService = <<<'CONF'

# Dict service for ACL sharing
service dict {
  unix_listener dict {
    mode = 0660
    user = dovecot
    group = dovecot
  }
}
CONF;
                file_put_contents($masterConf, $content.$dictService);
                $this->line('  Added dict service to 10-master.conf');
            } else {
                $this->line('  Dict service already present in 10-master.conf');
            }
        }

        // Restart Dovecot
        $this->info('  Restarting Dovecot...');
        try {
            $process = new Process(['systemctl', 'restart', 'dovecot']);
            $process->run();
            if ($process->isSuccessful()) {
                $this->line('  Dovecot restarted successfully');
            } else {
                $this->warn('  Failed to restart Dovecot: '.$process->getErrorOutput());
            }
        } catch (Exception $e) {
            $this->warn('  Failed to restart Dovecot: '.$e->getMessage());
        }

        $this->newLine();
        $this->info('Dovecot ACL configuration complete.');

        return 0;
    }
}
