<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Support\Facades\DB;

return new class extends Migration
{
    public function up(): void
    {
        DB::table('dns_settings')->updateOrInsert(
            ['key' => 'default_ipv6'],
            ['value' => null, 'created_at' => now(), 'updated_at' => now()]
        );
    }

    public function down(): void
    {
        DB::table('dns_settings')->where('key', 'default_ipv6')->delete();
    }
};
