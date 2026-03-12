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

        // Read DB credentials
        $dbPass = '';
        $dbHost = 'localhost';
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
            $dbHost = config('database.connections.mysql.host', 'localhost');
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

        // Configure ACL dict
        $this->info('  Configuring ACL dict...');
        // Dovecot 2.4: inline dict in dict_server
        $dictServerConf = '/etc/dovecot/conf.d/30-dict-server.conf';
        if (file_exists($dictServerConf)) {
            $content = file_get_contents($dictServerConf);
            if (! str_contains($content, 'dict acl')) {
                $dictBlock = <<<CONF
  dict acl {
    driver = sql
    sql_driver = mysql

    mysql {$dbHost} {
      dbname = {$dbName}
      user = {$dbUser}
      password = {$dbPass}
    }

    dict_map shared/shared-boxes/user/\$to/\$from {
      sql_table = user_shares
      value_field dummy {
      }

      key_field from_user {
        value = \$from
      }
      key_field to_user {
        value = \$to
      }
    }
  }
CONF;
                $content = preg_replace(
                    '/^(dict_server\s*\{.*?)(^\})/ms',
                    "$1\n".$dictBlock."\n$2",
                    $content
                );
                file_put_contents($dictServerConf, $content);
                $this->line('  Added ACL dict to 30-dict-server.conf');
            } else {
                $this->line('  ACL dict already present in 30-dict-server.conf');
            }
        } else {
            $this->error('  30-dict-server.conf not found');

            return 1;
        }

        // Ensure dict service socket exists in 10-master.conf
        $this->info('  Configuring dict service...');
        $masterConf = '/etc/dovecot/conf.d/10-master.conf';
        if (file_exists($masterConf)) {
            $content = file_get_contents($masterConf);
            if (! str_contains($content, 'service dict')) {
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
