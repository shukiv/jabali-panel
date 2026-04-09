<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::table('hosting_packages', function (Blueprint $table) {
            $table->unsignedInteger('nginx_req_per_sec')->nullable()->after('max_processes')
                ->comment('Nginx rate limit: requests per second per IP');
            $table->unsignedInteger('nginx_connections')->nullable()->after('nginx_req_per_sec')
                ->comment('Nginx connection limit: max concurrent connections per IP');
        });

        Schema::table('users', function (Blueprint $table) {
            $table->unsignedInteger('nginx_req_per_sec')->nullable()->after('max_processes')
                ->comment('Per-user nginx rate limit override, null = inherit from package');
            $table->unsignedInteger('nginx_connections')->nullable()->after('nginx_req_per_sec')
                ->comment('Per-user nginx connection limit override, null = inherit from package');
        });
    }

    public function down(): void
    {
        Schema::table('hosting_packages', function (Blueprint $table) {
            $table->dropColumn(['nginx_req_per_sec', 'nginx_connections']);
        });

        Schema::table('users', function (Blueprint $table) {
            $table->dropColumn(['nginx_req_per_sec', 'nginx_connections']);
        });
    }
};
