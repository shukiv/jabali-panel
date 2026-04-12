<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    /**
     * Unified background task tracking — see docs/adr/0007-agent-v2-control-plane.md.
     *
     * Source of truth for every long-running operation: backups, SSL, addon
     * installs, migrations, deploys. The Laravel queue, the agent, and the UI
     * all read/write this table.
     */
    public function up(): void
    {
        Schema::create('background_tasks', function (Blueprint $table): void {
            $table->uuid('id')->primary();

            $table->string('type', 40);
            $table->string('target_type', 191)->nullable();
            $table->string('target_id', 191)->nullable();

            // pending | running | done | failed | canceled
            $table->string('status', 16)->default('pending');

            $table->unsignedTinyInteger('progress')->nullable();
            $table->string('step', 120)->nullable();

            $table->timestamp('started_at')->nullable();
            $table->timestamp('finished_at')->nullable();
            $table->timestamps();

            $table->string('systemd_unit', 120)->nullable();
            $table->unsignedInteger('pid')->nullable();
            $table->integer('exit_code')->nullable();
            $table->text('error_message')->nullable();

            $table->unsignedSmallInteger('retries')->default(0);
            $table->unsignedSmallInteger('max_retries')->default(0);

            $table->json('payload')->nullable();
            $table->string('log_path', 255);
            $table->string('callback_token', 128);
            $table->string('dedupe_key', 191)->nullable();

            // Dashboard queries: "show latest 50 running tasks", "filter by type"
            $table->index(['status', 'type', 'created_at'], 'bg_tasks_status_type_created_idx');

            // Policy checks: "does this domain already have an SSL task running?"
            $table->index(['target_type', 'target_id', 'status'], 'bg_tasks_target_status_idx');

            // Uniqueness on dedupe_key is enforced at the application layer
            // via BackgroundTask::tryCreateUnique() because we only want
            // "no duplicate active rows" — a later row with the same key
            // but status=done should be allowed. Partial/filtered unique
            // indexes are not portable across MariaDB and Postgres.
            $table->index('dedupe_key', 'bg_tasks_dedupe_idx');
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('background_tasks');
    }
};
