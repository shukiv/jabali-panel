<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    /**
     * Run the migrations.
     */
    public function up(): void
    {
        Schema::table('email_domains', function (Blueprint $table) {
            $table->bigInteger('max_mailboxes')->nullable()->default(null)->change();
        });

        // Set existing hardcoded-10 rows to NULL (unlimited) so hosting package limit is the single source of truth
        DB::table('email_domains')->where('max_mailboxes', 10)->update(['max_mailboxes' => null]);
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        DB::table('email_domains')->whereNull('max_mailboxes')->update(['max_mailboxes' => 10]);

        Schema::table('email_domains', function (Blueprint $table) {
            $table->bigInteger('max_mailboxes')->default(10)->change();
        });
    }
};
