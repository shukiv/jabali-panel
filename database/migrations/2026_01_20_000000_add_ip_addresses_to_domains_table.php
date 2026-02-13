<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::table('domains', function (Blueprint $table) {
            $table->string('ip_address', 45)->nullable()->after('document_root');
            $table->string('ipv6_address', 45)->nullable()->after('ip_address');
        });
    }

    public function down(): void
    {
        Schema::table('domains', function (Blueprint $table) {
            $table->dropColumn(['ip_address', 'ipv6_address']);
        });
    }
};
