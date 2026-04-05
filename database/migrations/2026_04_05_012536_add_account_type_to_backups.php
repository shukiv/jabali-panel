<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Support\Facades\DB;

return new class extends Migration
{
    public function up(): void
    {
        DB::statement("ALTER TABLE backups MODIFY COLUMN type ENUM('full','partial','server','account') NOT NULL");
    }

    public function down(): void
    {
        DB::statement("ALTER TABLE backups MODIFY COLUMN type ENUM('full','partial','server') NOT NULL");
    }
};
