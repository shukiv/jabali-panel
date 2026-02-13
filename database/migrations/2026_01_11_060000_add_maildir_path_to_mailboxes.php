<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    /**
     * Run the migrations.
     */
    public function up(): void
    {
        Schema::table('mailboxes', function (Blueprint $table) {
            $table->string('maildir_path', 500)->nullable()->after('password_encrypted');
            $table->unsignedInteger('system_uid')->nullable()->after('maildir_path');
            $table->unsignedInteger('system_gid')->nullable()->after('system_uid');
        });
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        Schema::table('mailboxes', function (Blueprint $table) {
            $table->dropColumn(['maildir_path', 'system_uid', 'system_gid']);
        });
    }
};
