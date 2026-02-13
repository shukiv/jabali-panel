<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\HostingPackages\Pages;

use App\Filament\Admin\Resources\HostingPackages\HostingPackageResource;
use Filament\Resources\Pages\CreateRecord;

class CreateHostingPackage extends CreateRecord
{
    protected static string $resource = HostingPackageResource::class;
}
