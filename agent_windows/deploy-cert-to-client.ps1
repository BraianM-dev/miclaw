#Requires -RunAsAdministrator
<#
.SYNOPSIS
    Importa frank-codesign.cer al almacén Trusted Publishers del equipo cliente.

.DESCRIPTION
    Ejecutar una vez en cada PC donde se desplegará Frank.
    Después de importar el certificado, Windows Defender / SmartScreen
    reconoce los ejecutables firmados por AFE IT Systems como de confianza
    y no muestra alertas de "amenaza desconocida".

    Alternativa sin ejecutar este script:
      - GPO: Computer Configuration > Windows Settings > Security Settings
             > Public Key Policies > Trusted Publishers > Import > frank-codesign.cer

.PARAMETER CertPath
    Ruta al archivo frank-codesign.cer (por defecto en la misma carpeta que este script).
#>

param(
    [string]$CertPath = (Join-Path $PSScriptRoot "frank-codesign.cer")
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if (-not (Test-Path $CertPath)) {
    Write-Error "No se encontró el certificado en: $CertPath`nGenera el certificado ejecutando sign-frank.ps1 primero."
    exit 1
}

$cert = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2($CertPath)

# Importar a Trusted Publishers (LocalMachine)
$store = New-Object System.Security.Cryptography.X509Certificates.X509Store(
    "TrustedPublisher", "LocalMachine")
$store.Open("ReadWrite")
if ($store.Certificates | Where-Object { $_.Thumbprint -eq $cert.Thumbprint }) {
    Write-Host "[OK] El certificado ya está en Trusted Publishers." -ForegroundColor Green
} else {
    $store.Add($cert)
    Write-Host "[OK] Certificado importado a Trusted Publishers." -ForegroundColor Green
}
$store.Close()

# Importar también a Trusted Root (necesario para SmartScreen en algunos casos)
$rootStore = New-Object System.Security.Cryptography.X509Certificates.X509Store(
    "Root", "LocalMachine")
$rootStore.Open("ReadWrite")
if (-not ($rootStore.Certificates | Where-Object { $_.Thumbprint -eq $cert.Thumbprint })) {
    $rootStore.Add($cert)
    Write-Host "[OK] Certificado importado a Trusted Root CA." -ForegroundColor Green
} else {
    Write-Host "[OK] Ya estaba en Trusted Root CA." -ForegroundColor Green
}
$rootStore.Close()

Write-Host ""
Write-Host "Certificado: $($cert.Subject)" -ForegroundColor Cyan
Write-Host "Válido hasta: $($cert.NotAfter.ToString('yyyy-MM-dd'))" -ForegroundColor Cyan
Write-Host ""
Write-Host "Los ejecutables Frank firmados con este certificado ya no mostrarán" -ForegroundColor Green
Write-Host "alertas de Windows Defender en este equipo." -ForegroundColor Green
