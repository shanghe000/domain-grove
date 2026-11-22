import ssl

import pytest

import config_downloader
from settings import Settings, WorkerError


BASE = "https://config.example.test/open/config/download"
PROJECT = "00000000-0000-4000-8000-000000000001"


def test_scoped_id_uses_original_filename_and_canonical_source(monkeypatch):
    approved = Settings(mock=False, config_base=BASE, config_project_id=PROJECT, config_environment="development")
    link = f"{BASE}/142?env=dev&project_id={PROJECT}"

    def fetch(url, **options):
        assert url == link
        assert options["verify"] is True
        assert options["headers"] == {}
        options["response_headers"]["content-disposition"] = (
            "attachment; filename*=utf-8''%E6%B4%BB%E5%8A%A8.xlsx"
        )
        return b"fixture workbook"

    monkeypatch.setattr(config_downloader, "fetch", fetch)
    assert config_downloader.config_candidate(approved, link)
    assert config_downloader.download(approved, link) == (
        "活动.xlsx", b"fixture workbook", f"{BASE}/142?project_id={PROJECT}&env=development",
    )


@pytest.mark.parametrize("query", [
    "env=dev&project_id=00000000-0000-4000-8000-000000000002",
    f"env=prod&project_id={PROJECT}",
    f"env=dev&env=master&project_id={PROJECT}",
    f"env=dev&project_id={PROJECT}&project_code=example_other",
    "env=dev&project_code=example_other",
])
def test_scope_rejected_before_fetch(monkeypatch, query):
    approved = Settings(mock=False, config_base=BASE, config_project_id=PROJECT, config_environment="development")
    monkeypatch.setattr(config_downloader, "fetch", lambda *a, **k: pytest.fail("out-of-scope network call"))
    with pytest.raises(WorkerError) as error:
        config_downloader.download(approved, f"{BASE}/142?{query}")
    assert error.value.status == 403


def test_custom_ca_keeps_host_validation_enabled(monkeypatch, tmp_path):
    anchor = tmp_path / "trusted-ca.pem"
    context = ssl.create_default_context()
    monkeypatch.setattr(config_downloader.ssl, "create_default_context", lambda *, cafile: context if cafile == str(anchor) else None)
    selected = config_downloader.config_trust(str(anchor))
    assert selected is context and selected.check_hostname and selected.verify_mode == ssl.CERT_REQUIRED


def test_attachment_name_cannot_name_external_file(monkeypatch):
    def fetch(_url, **options):
        options["response_headers"]["content-disposition"] = 'attachment; filename="../outside.xlsx"'
        return b"fixture workbook"

    monkeypatch.setattr(config_downloader, "fetch", fetch)
    with pytest.raises(WorkerError, match="有效的 .xlsx 文件名"):
        config_downloader.download(Settings(config_base=BASE, mock=False), f"{BASE}/1")
