<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('server_imports', function (Blueprint $table) {
            $table->id();
            $table->string('name')->comment('Import job name');
            $table->enum('source_type', ['cpanel', 'directadmin'])->default('cpanel');
            $table->enum('import_method', ['backup_file', 'remote_server'])->default('backup_file');
            $table->string('backup_path')->nullable()->comment('Path to uploaded backup file');
            $table->string('remote_host')->nullable();
            $table->integer('remote_port')->nullable();
            $table->string('remote_user')->nullable();
            $table->text('remote_password')->nullable()->comment('Encrypted');
            $table->string('remote_api_token')->nullable()->comment('Encrypted API token');
            $table->json('discovered_accounts')->nullable()->comment('List of accounts found');
            $table->json('selected_accounts')->nullable()->comment('Accounts selected for import');
            $table->json('import_options')->nullable()->comment('What to import: files, databases, emails, dns');
            $table->enum('status', ['pending', 'discovering', 'ready', 'importing', 'completed', 'failed', 'cancelled'])->default('pending');
            $table->integer('progress')->default(0)->comment('0-100 progress percentage');
            $table->string('current_task')->nullable()->comment('Current task being processed');
            $table->json('import_log')->nullable()->comment('Log of import actions');
            $table->json('errors')->nullable()->comment('Any errors encountered');
            $table->timestamp('started_at')->nullable();
            $table->timestamp('completed_at')->nullable();
            $table->timestamps();
        });

        Schema::create('server_import_accounts', function (Blueprint $table) {
            $table->id();
            $table->foreignId('server_import_id')->constrained()->onDelete('cascade');
            $table->string('source_username')->comment('Username in source server');
            $table->string('target_username')->nullable()->comment('Username in Jabali');
            $table->string('email')->nullable();
            $table->string('main_domain')->nullable();
            $table->json('addon_domains')->nullable();
            $table->json('subdomains')->nullable();
            $table->json('databases')->nullable();
            $table->json('email_accounts')->nullable();
            $table->bigInteger('disk_usage')->default(0)->comment('Bytes');
            $table->enum('status', ['pending', 'importing', 'completed', 'failed', 'skipped'])->default('pending');
            $table->integer('progress')->default(0);
            $table->string('current_task')->nullable();
            $table->json('import_log')->nullable();
            $table->text('error')->nullable();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('server_import_accounts');
        Schema::dropIfExists('server_imports');
    }
};
