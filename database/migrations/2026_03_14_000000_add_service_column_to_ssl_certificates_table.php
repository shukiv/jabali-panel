<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::table('ssl_certificates', function (Blueprint $table) {
            $table->string('service', 10)->default('web')->after('domain_id');

            // Drop old index if exists, add composite unique
            $table->unique(['domain_id', 'service'], 'ssl_certificates_domain_service_unique');
        });
    }

    public function down(): void
    {
        Schema::table('ssl_certificates', function (Blueprint $table) {
            $table->dropUnique('ssl_certificates_domain_service_unique');
            $table->dropColumn('service');
        });
    }
};
