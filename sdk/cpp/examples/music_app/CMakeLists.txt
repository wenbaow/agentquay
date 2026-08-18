add_executable(music_app
    main.cpp
    MusicController.h
)

target_link_libraries(music_app PRIVATE agentquay::agentquay)

if(AGENTQUAY_WITH_WIDGETS)
    # 演示 GUI 确认框：也可用 QCoreApplication + 控制台确认（无需 Widgets）
    target_link_libraries(music_app PRIVATE Qt6::Widgets)
endif()