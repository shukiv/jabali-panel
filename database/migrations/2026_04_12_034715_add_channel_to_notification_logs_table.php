<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        if (! Schema::hasColumn('notification_logs', 'channel')) {
            Schema::table('notification_logs', function (Blueprint $table) {
                // Which channel delivered this entry. Nullable for pre-v2 rows,
                // which are backfilled to 'email' below.
                $table->string('channel', 50)->nullable()->after('status')->index();
            });
        }

        // Backfill legacy rows — they all came through the email-only path.
        DB::table('notification_logs')
            ->whereNull('channel')
            ->update(['channel' => 'email']);
    }

    public function down(): void
    {
        Schema::table('notification_logs', function (Blueprint $table) {
            if (Schema::hasColumn('notification_logs', 'channel')) {
                $table->dropIndex(['channel']);
                $table->dropColumn('channel');
            }
        });
    }
};
