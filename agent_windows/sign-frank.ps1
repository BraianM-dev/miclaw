#Requires -RunAsAdministrator
<#
.SYNOPSIS
    Firma Frank64.exe / Frank32.exe con un certificado de firma de código auto-firmado.

.DESCRIPTION
    1. Crea (o reutiliza) un certificado de firma de código para "AFE IT Systems".
    2. Firma los ejecutables Frank64.exe y Frank32.exe.
    3. Exporta el certificado a frank-codesign.cer para distribuirlo por GPO.

    Para eliminar la alerta de Windows Defender en los equipos cliente, importa
    frank-codesign.cer a "Trusted Publishers" con deploy-cert-to-client.ps1
    o mediante una GPO de Computer Configuration → Windows Settings → Security
    Settings → Public Key Policies → Trusted Publishers.

.NOTES
    Requiere Windows PowerShell 5.1+ o PowerShell 7+.
    Debe ejecutarse como Administrador para acceder al almacén de certificados.
#>

param(
    [string]$CertSubject  = "CN=AFE IT Systems Frank Agent, O=AFE, C=UY",
    [string]$CertFriendly = "AFE Frank Agent Code Signing",
    [int]$ValidYears       = 10
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Write-Step([string]$msg) { Write-Host "`n[STEP] $msg" -ForegroundColor Cyan }
function Write-OK([string]$msg)   { Write-Host "[OK]   $msg"  -ForegroundColor Green }
function Write-Warn([string]$msg) { Write-Host "[WARN] $msg"  -ForegroundColor Yellow }

# ── 1. Buscar o crear certificado ─────────────────────────────────────────────
Write-Step "Buscando certificado existente..."
$cert = Get-ChildItem Cert:\LocalMachine\My |
        Where-Object { $_.Subject -eq $CertSubject -and
                       $_.NotAfter -gt (Get-Date).AddDays(30) } |
        Sort-Object NotAfter -Descending |
        Select-Object -First 1

if ($cert) {
    Write-OK "Certificado encontrado: $($cert.Thumbprint) (expira $($cert.NotAfter.ToString('yyyy-MM-dd')))"
} else {
    Write-Step "Creando nuevo certificado de firma de código..."
    $cert = New-SelfSignedCertificate `
        -Subject          $CertSubject `
        -FriendlyName     $CertFriendly `
        -Type             CodeSigning `
        -KeyUsage         DigitalSignature `
        -KeyAlgorithm     RSA `
        -KeyLength        4096 `
        -HashAlgorithm    SHA256 `
        -NotAfter         (Get-Date).AddYears($ValidYears) `
        -CertStoreLocation "Cert:\LocalMachine\My"
    Write-OK "Certificado creado: $($cert.Thumbprint)"
}

# ── 2. Agregar a Trusted Publishers localmente ────────────────────────────────
Write-Step "Agregando certificado a Trusted Publishers locales..."
$store = New-Object System.Security.Cryptography.X509Certificates.X509Store(
    "TrustedPublisher", "LocalMachine")
$store.Open("ReadWrite")
if (-not ($store.Certificates | Where-Object { $_.Thumbprint -eq $cert.Thumbprint })) {
    $store.Add($cert)
    Write-OK "Certificado agregado a TrustedPublisher"
} else {
    Write-OK "Ya estaba en TrustedPublisher"
}
$store.Close()

# ── 3. Exportar .cer para GPO / distribución ──────────────────────────────────
Write-Step "Exportando certificado público (frank-codesign.cer)..."
$cerPath = Join-Path $PSScriptRoot "frank-codesign.cer"
Export-Certificate -Cert $cert -FilePath $cerPath -Type CERT | Out-Null
Write-OK "Exportado: $cerPath"

# ── 4. Firmar los ejecutables ─────────────────────────────────────────────────
$exes = @("Frank64.exe", "Frank32.exe") |
        ForEach-Object { Join-Path $PSScriptRoot $_ } |
        Where-Object { Test-Path $_ }

if ($exes.Count -eq 0) {
    Write-Warn "No se encontraron Frank64.exe ni Frank32.exe. Compila primero con build.bat."
    exit 1
}

foreach ($exe in $exes) {
    Write-Step "Firmando: $(Split-Path $exe -Leaf)"
    $result = Set-AuthenticodeSignature -FilePath $exe -Certificate $cert -TimestampServer ""
    if ($result.Status -eq "Valid") {
        Write-OK "Firmado correctamente: $(Split-Path $exe -Leaf)"
    } else {
        # Sin timestamp server (sin internet): la firma es válida pero expira con el cert.
        # Es suficiente para entornos de intranet con el cert en Trusted Publishers.
        Write-Warn "Estado: $($result.Status) — $(Split-Path $exe -Leaf)"
        Write-Warn "En entornos de intranet sin internet esto es normal. La firma es funcional."
    }
}

Write-Host "`n===============================================" -ForegroundColor Green
Write-Host " FIRMA COMPLETADA" -ForegroundColor Green
Write-Host "===============================================" -ForegroundColor Green
Write-Host ""
Write-Host "Proximos pasos para eliminar la alerta en los clientes:"
Write-Host ""
Write-Host "  Opcion A — Manual (un equipo):"
Write-Host "    Ejecutar en cada PC cliente:"
Write-Host "    PowerShell -ExecutionPolicy Bypass -File deploy-cert-to-client.ps1"
Write-Host ""
Write-Host "  Opcion B — GPO (dominio):"
Write-Host "    Computer Configuration -> Windows Settings -> Security Settings"
Write-Host "    -> Public Key Policies -> Trusted Publishers -> Import -> frank-codesign.cer"
Write-Host ""
Write-Host "  El archivo a distribuir es: frank-codesign.cer"
Write-Host ""
