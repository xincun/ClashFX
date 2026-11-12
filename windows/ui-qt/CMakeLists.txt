cmake_minimum_required(VERSION 3.24)
project(clashfx-ui VERSION 1.19.24 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTORCC ON)
set(CMAKE_AUTOUIC ON)

if(MSVC)
    add_compile_options(/utf-8 /W3)
endif()

find_package(Qt6 REQUIRED COMPONENTS Core Gui Widgets Network Svg)

set(WEBVIEW2_SDK_DIR "" CACHE PATH "Path to extracted Microsoft.Web.WebView2 NuGet root")
if(NOT WEBVIEW2_SDK_DIR)
    set(WEBVIEW2_SDK_DIR "${CMAKE_CURRENT_SOURCE_DIR}/../build/.cache/webview2")
endif()

if(CMAKE_SYSTEM_PROCESSOR MATCHES "ARM64|aarch64")
    set(DEFAULT_WEBVIEW2_ARCH "arm64")
else()
    set(DEFAULT_WEBVIEW2_ARCH "x64")
endif()
set(WEBVIEW2_ARCH "${DEFAULT_WEBVIEW2_ARCH}" CACHE STRING "WebView2 static loader arch (arm64 or x64)")

set(WEBVIEW2_INCLUDE_DIR "${WEBVIEW2_SDK_DIR}/build/native/include")
set(WEBVIEW2_STATIC_LIB "${WEBVIEW2_SDK_DIR}/build/native/${WEBVIEW2_ARCH}/WebView2LoaderStatic.lib")

if(NOT EXISTS "${WEBVIEW2_INCLUDE_DIR}/WebView2.h")
    message(FATAL_ERROR "WebView2.h not found at ${WEBVIEW2_INCLUDE_DIR}. Run python3 windows/scripts/fetch-webview2-sdk.py first.")
endif()
if(NOT EXISTS "${WEBVIEW2_STATIC_LIB}")
    message(FATAL_ERROR "WebView2LoaderStatic.lib not found at ${WEBVIEW2_STATIC_LIB}.")
endif()

set(CLASHFX_UI_SOURCES
    src/main.cpp
    src/I18n.h
    src/I18n.cpp
    src/DaemonClient.h
    src/DaemonClient.cpp
    src/TrayIconGenerator.h
    src/TrayIconGenerator.cpp
    src/HotkeyManager.h
    src/HotkeyManager.cpp
    src/WebView2Widget.h
    src/WebView2Widget.cpp
    src/ConsoleWindow.h
    src/ConsoleWindow.cpp
    src/AiLockAndNodeDialogs.h
    src/AiLockAndNodeDialogs.cpp
    src/SubscriptionsAndSettingsDialogs.h
    src/SubscriptionsAndSettingsDialogs.cpp
    src/TrayController.h
    src/TrayController.cpp
    resources/clashfx.qrc
    resources/clashfx.rc
)

add_executable(clashfx-ui WIN32 ${CLASHFX_UI_SOURCES})

target_include_directories(clashfx-ui PRIVATE
    "${CMAKE_CURRENT_SOURCE_DIR}/src"
    "${WEBVIEW2_INCLUDE_DIR}"
)

target_link_libraries(clashfx-ui PRIVATE
    Qt6::Core
    Qt6::Gui
    Qt6::Widgets
    Qt6::Network
    Qt6::Svg
    "${WEBVIEW2_STATIC_LIB}"
    user32
    shell32
    ole32
    advapi32
    shlwapi
    version
)
