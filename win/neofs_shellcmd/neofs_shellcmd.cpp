// neofs_shellcmd.dll — IExplorerCommand handlers so NeoFS tray verbs appear on the
// Windows 11 primary File Explorer context menu (not only "Show more options").
// Build: see Makefile target neofs-shellcmd.dll (MinGW g++).

#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <shellapi.h>
#include <shobjidl.h>
#include <shlwapi.h>
#include <stdio.h>
#include <cstring>
#include <new>

#ifndef ARRAYSIZE
#define ARRAYSIZE(a) ((int)(sizeof(a) / sizeof((a)[0])))
#endif

#pragma comment(lib, "ole32.lib")
#pragma comment(lib, "shell32.lib")
#pragma comment(lib, "shlwapi.lib")
#pragma comment(lib, "kernel32.lib")

// Must match internal/explorerpin/shellverb_windows.go (RegisterNeoFSExplorerCommands).
// {7E4F8C21-0A3D-4B91-BE6D-8C2A9F55D401}
static const CLSID CLSID_NeoFSAttrsVerb = {
    0x7e4f8c21, 0x0a3d, 0x4b91, {0xbe, 0x6d, 0x8c, 0x2a, 0x9f, 0x55, 0xd4, 0x01}};
// {8F5A9D32-1B4E-4CA2-CF7E-9D3B0A66E512}
static const CLSID CLSID_NeoFSDeleteVerb = {
    0x8f5a9d32, 0x1b4e, 0x4ca2, {0xcf, 0x7e, 0x9d, 0x3b, 0x0a, 0x66, 0xe5, 0x12}};

static const WCHAR kRegConfigKey[] =
    L"Software\\neofs-mount\\ShellExtension";
static const WCHAR kRegTrayExe[] = L"TrayExe";

static LONG g_cServer = 0;
static LONG g_cLock = 0;

static HRESULT ReadTrayExePath(WCHAR *buf, DWORD cchBuf) {
    HKEY hkey = nullptr;
    LSTATUS st = RegOpenKeyExW(HKEY_CURRENT_USER, kRegConfigKey, 0, KEY_READ, &hkey);
    if (st != ERROR_SUCCESS)
        return HRESULT_FROM_WIN32(st);
    DWORD type = 0;
    DWORD cb = cchBuf * sizeof(WCHAR);
    st = RegQueryValueExW(hkey, kRegTrayExe, nullptr, &type, (LPBYTE)buf, &cb);
    RegCloseKey(hkey);
    if (st != ERROR_SUCCESS)
        return HRESULT_FROM_WIN32(st);
    if (type != REG_SZ && type != REG_EXPAND_SZ)
        return E_FAIL;
    if (cb < sizeof(WCHAR) || buf[cb / sizeof(WCHAR) - 1] != L'\0')
        return E_FAIL;
    if (type == REG_EXPAND_SZ) {
        WCHAR expanded[32768];
        DWORD n = ExpandEnvironmentStringsW(buf, expanded, (DWORD)(ARRAYSIZE(expanded)));
        if (n == 0 || n > ARRAYSIZE(expanded))
            return HRESULT_FROM_WIN32(GetLastError());
        if (n > cchBuf)
            return HRESULT_FROM_WIN32(ERROR_INSUFFICIENT_BUFFER);
        memcpy(buf, expanded, n * sizeof(WCHAR));
    }
    return S_OK;
}

// Cloud / library folders often fail SIGDN_FILESYSPATH; parsing name usually still works.
static HRESULT GetFsPathFromShellItem(IShellItem *item, PWSTR *outPath) {
    *outPath = nullptr;
    static const SIGDN kTry[] = {SIGDN_FILESYSPATH, SIGDN_DESKTOPABSOLUTEPARSING};
    for (SIGDN sig : kTry) {
        PWSTR p = nullptr;
        HRESULT hr = item->GetDisplayName(sig, &p);
        if (SUCCEEDED(hr) && p && p[0] != L'\0') {
            *outPath = p;
            return S_OK;
        }
        if (p)
            CoTaskMemFree(p);
    }
    return HRESULT_FROM_WIN32(ERROR_PATH_NOT_FOUND);
}

// ShellExecuteEx from IExplorerCommand can be unreliable; CreateProcessW with a writable
// command line matches how the shell launches child processes.
static HRESULT LaunchTrayWithCliAndPath(const WCHAR *trayExe, const WCHAR *cliFlag,
                                        const WCHAR *path) {
    size_t need = wcslen(trayExe) + wcslen(cliFlag) + wcslen(path) + 64;
    WCHAR *cmdLine = (WCHAR *)CoTaskMemAlloc(need * sizeof(WCHAR));
    if (!cmdLine)
        return E_OUTOFMEMORY;
    int nw = swprintf(cmdLine, need, L"\"%s\" %s \"%s\"", trayExe, cliFlag, path);
    if (nw < 0 || (size_t)nw >= need) {
        CoTaskMemFree(cmdLine);
        return E_FAIL;
    }

    STARTUPINFOW si{};
    si.cb = sizeof(si);
    PROCESS_INFORMATION pi{};
    BOOL ok = CreateProcessW(trayExe, cmdLine, nullptr, nullptr, FALSE, 0, nullptr, nullptr, &si,
                             &pi);
    DWORD gle = ok ? 0 : GetLastError();
    CoTaskMemFree(cmdLine);
    if (!ok)
        return HRESULT_FROM_WIN32(gle);
    CloseHandle(pi.hThread);
    CloseHandle(pi.hProcess);
    return S_OK;
}

class NeoFSExplorerCommand final : public IExplorerCommand, public IObjectWithSite {
public:
    explicit NeoFSExplorerCommand(REFCLSID clsidSelf, const WCHAR *title, const WCHAR *cliFlag)
        : _cRef(1), _clsidSelf(clsidSelf), _title(title), _cliFlag(cliFlag) {
        InterlockedIncrement(&g_cServer);
    }

    ~NeoFSExplorerCommand() {
        if (_site)
            _site->Release();
        InterlockedDecrement(&g_cServer);
    }

    // IUnknown (canonical identity: IExplorerCommand*)
    IFACEMETHODIMP QueryInterface(REFIID riid, void **ppv) {
        if (!ppv)
            return E_POINTER;
        *ppv = nullptr;
        if (riid == IID_IUnknown)
            *ppv = static_cast<IExplorerCommand *>(this);
        else if (riid == IID_IExplorerCommand)
            *ppv = static_cast<IExplorerCommand *>(this);
        else if (riid == IID_IObjectWithSite)
            *ppv = static_cast<IObjectWithSite *>(this);
        else
            return E_NOINTERFACE;
        AddRef();
        return S_OK;
    }
    IFACEMETHODIMP_(ULONG) AddRef() { return (ULONG)InterlockedIncrement(&_cRef); }
    IFACEMETHODIMP_(ULONG) Release() {
        ULONG c = (ULONG)InterlockedDecrement(&_cRef);
        if (!c)
            delete this;
        return c;
    }

    // IExplorerCommand
    IFACEMETHODIMP GetTitle(IShellItemArray *, LPWSTR *ppszName) {
        return SHStrDupW(_title, ppszName);
    }
    IFACEMETHODIMP GetIcon(IShellItemArray *, LPWSTR *ppszIcon) {
        *ppszIcon = nullptr;
        return E_NOTIMPL;
    }
    IFACEMETHODIMP GetToolTip(IShellItemArray *, LPWSTR *ppszInfotip) {
        *ppszInfotip = nullptr;
        return E_NOTIMPL;
    }
    IFACEMETHODIMP GetCanonicalName(GUID *pguidCommandName) {
        *pguidCommandName = _clsidSelf;
        return S_OK;
    }
    IFACEMETHODIMP GetState(IShellItemArray *psiItemArray, BOOL /*fOkToBeSlow*/,
                            EXPCMDSTATE *pCmdState) {
        if (!psiItemArray) {
            *pCmdState = ECS_DISABLED;
            return S_OK;
        }
        DWORD n = 0;
        HRESULT hr = psiItemArray->GetCount(&n);
        if (FAILED(hr)) {
            *pCmdState = ECS_DISABLED;
            return S_OK;
        }
        *pCmdState = (n == 1) ? ECS_ENABLED : ECS_DISABLED;
        return S_OK;
    }
    IFACEMETHODIMP Invoke(IShellItemArray *psiItemArray, IBindCtx *) {
        if (!psiItemArray)
            return E_INVALIDARG;
        DWORD n = 0;
        HRESULT hr = psiItemArray->GetCount(&n);
        if (FAILED(hr) || n != 1)
            return E_FAIL;

        IShellItem *item = nullptr;
        hr = psiItemArray->GetItemAt(0, &item);
        if (FAILED(hr) || !item)
            return hr;

        PWSTR path = nullptr;
        hr = GetFsPathFromShellItem(item, &path);
        item->Release();
        if (FAILED(hr) || !path)
            return FAILED(hr) ? hr : E_FAIL;

        WCHAR trayExe[32768];
        hr = ReadTrayExePath(trayExe, (DWORD)(ARRAYSIZE(trayExe)));
        if (FAILED(hr)) {
            CoTaskMemFree(path);
            return hr;
        }

        hr = LaunchTrayWithCliAndPath(trayExe, _cliFlag, path);
        CoTaskMemFree(path);
        return hr;
    }
    IFACEMETHODIMP GetFlags(EXPCMDFLAGS *pFlags) {
        *pFlags = ECF_DEFAULT;
        return S_OK;
    }
    IFACEMETHODIMP EnumSubCommands(IEnumExplorerCommand **ppEnum) {
        *ppEnum = nullptr;
        return E_NOTIMPL;
    }

    // IObjectWithSite (Explorer may set a site on modern menu hosts)
    IFACEMETHODIMP SetSite(IUnknown *pUnkSite) {
        if (_site) {
            _site->Release();
            _site = nullptr;
        }
        _site = pUnkSite;
        if (_site)
            _site->AddRef();
        return S_OK;
    }
    IFACEMETHODIMP GetSite(REFIID riid, void **ppvSite) {
        if (!ppvSite)
            return E_POINTER;
        *ppvSite = nullptr;
        if (!_site)
            return E_FAIL;
        return _site->QueryInterface(riid, ppvSite);
    }

private:
    LONG _cRef;
    CLSID _clsidSelf;
    const WCHAR *_title;
    const WCHAR *_cliFlag;
    IUnknown *_site = nullptr;
};

class ClassFactory final : public IClassFactory {
public:
    explicit ClassFactory(REFCLSID clsid) : _cRef(1), _clsid(clsid) {}
    ~ClassFactory() = default;

    IFACEMETHODIMP QueryInterface(REFIID riid, void **ppv) {
        if (!ppv)
            return E_POINTER;
        *ppv = nullptr;
        if (riid == IID_IUnknown || riid == IID_IClassFactory) {
            *ppv = static_cast<IClassFactory *>(this);
            AddRef();
            return S_OK;
        }
        return E_NOINTERFACE;
    }
    IFACEMETHODIMP_(ULONG) AddRef() { return (ULONG)InterlockedIncrement(&_cRef); }
    IFACEMETHODIMP_(ULONG) Release() {
        ULONG c = (ULONG)InterlockedDecrement(&_cRef);
        if (!c)
            delete this;
        return c;
    }
    IFACEMETHODIMP CreateInstance(IUnknown *punkOuter, REFIID riid, void **ppv) {
        if (!ppv)
            return E_POINTER;
        *ppv = nullptr;
        if (punkOuter)
            return CLASS_E_NOAGGREGATION;

        NeoFSExplorerCommand *cmd = nullptr;
        if (IsEqualCLSID(_clsid, CLSID_NeoFSAttrsVerb)) {
            cmd = new (std::nothrow) NeoFSExplorerCommand(CLSID_NeoFSAttrsVerb,
                                                          L"NeoFS object details…",
                                                          L"-neofs-attrs");
        } else if (IsEqualCLSID(_clsid, CLSID_NeoFSDeleteVerb)) {
            cmd = new (std::nothrow) NeoFSExplorerCommand(CLSID_NeoFSDeleteVerb,
                                                          L"Delete from NeoFS container…",
                                                          L"-neofs-delete");
        } else
            return CLASS_E_CLASSNOTAVAILABLE;

        if (!cmd)
            return E_OUTOFMEMORY;

        HRESULT hr = cmd->QueryInterface(riid, ppv);
        cmd->Release();
        return hr;
    }
    IFACEMETHODIMP LockServer(BOOL fLock) {
        if (fLock)
            InterlockedIncrement(&g_cLock);
        else
            InterlockedDecrement(&g_cLock);
        return S_OK;
    }

private:
    LONG _cRef;
    CLSID _clsid;
};

extern "C" __declspec(dllexport) HRESULT __stdcall DllGetClassObject(REFCLSID rclsid, REFIID riid,
                                                                       void **ppv) {
    if (!ppv)
        return E_POINTER;
    *ppv = nullptr;

    if (!IsEqualCLSID(rclsid, CLSID_NeoFSAttrsVerb) &&
        !IsEqualCLSID(rclsid, CLSID_NeoFSDeleteVerb))
        return CLASS_E_CLASSNOTAVAILABLE;

    ClassFactory *fact = new (std::nothrow) ClassFactory(rclsid);
    if (!fact)
        return E_OUTOFMEMORY;
    HRESULT hr = fact->QueryInterface(riid, ppv);
    fact->Release();
    return hr;
}

extern "C" __declspec(dllexport) HRESULT __stdcall DllCanUnloadNow(void) {
    if (g_cServer == 0 && g_cLock == 0)
        return S_OK;
    return S_FALSE;
}

BOOL APIENTRY DllMain(HMODULE hModule, DWORD reason, LPVOID) {
    if (reason == DLL_PROCESS_ATTACH)
        DisableThreadLibraryCalls(hModule);
    return TRUE;
}
