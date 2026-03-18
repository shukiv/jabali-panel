<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::table('email_domains', function (Blueprint $table) {
            $table->string('stalwart_domain_id')->nullable()->after('dkim_public_key');
        });

        Schema::table('mailboxes', function (Blueprint $table) {
            $table->boolean('jmap_enabled')->default(true)->after('smtp_enabled');
        });
    }

    public function down(): void
    {
        Schema::table('email_domains', function (Blueprint $table) {
            $table->dropColumn('stalwart_domain_id');
        });

        Schema::table('mailboxes', function (Blueprint $table) {
            $table->dropColumn('jmap_enabled');
        });
    }
};
